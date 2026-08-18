package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestServer 起一个带临时数据库的完整 HTTP 服务，限流放宽到不影响用例。
func newTestServer(t *testing.T) (*server, *httptest.Server) {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	blob, err := newLocalBlob(t.TempDir())
	if err != nil {
		t.Fatalf("初始化附件目录失败: %v", err)
	}
	s := &server{
		cfg:        &config{BaseURL: "http://test.local", OnlineTimeout: 3 * time.Minute},
		store:      st,
		blob:       blob,
		rl:         newRateLimiter(1000, time.Minute),
		pullRl:     newRateLimiter(1000, time.Minute),
		sessionKey: "test-session-key",
	}
	srv := httptest.NewServer(s.routes())
	t.Cleanup(srv.Close)
	return s, srv
}

func adminSession(s *server) string {
	return s.sessionValue(time.Now().Add(time.Hour).Unix())
}

func newAgent(t *testing.T, s *server, name string) (*Agent, string) {
	t.Helper()
	a, raw, err := s.store.createAgent(name, "host-"+name, "Linux", "arm64", "10.0.0.1", "")
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	return a, raw
}

// doJSON 发请求并返回状态码与响应体。
func doJSON(t *testing.T, method, url string, body any, session, bearer string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data
}

// mustJSON 解析响应体为 map，失败即终止用例。
func mustJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("响应不是合法 JSON: %s", data)
	}
	return v
}

// createTask 以管理员身份创建任务，返回任务 ID。
func createTask(t *testing.T, srv *httptest.Server, sess string, agentIDs ...string) string {
	t.Helper()
	code, body := doJSON(t, "POST", srv.URL+"/api/tasks", map[string]any{
		"title": "巡检任务", "content": "检查磁盘占用并汇报", "agent_ids": agentIDs,
	}, sess, "")
	if code != http.StatusCreated {
		t.Fatalf("创建任务应 201，实际 %d: %s", code, body)
	}
	return mustJSON(t, body)["task"].(map[string]any)["id"].(string)
}

// pullOne 以 Agent 身份拉取任务，返回任务列表。
func pullTasks(t *testing.T, srv *httptest.Server, tok string) []any {
	t.Helper()
	code, body := doJSON(t, "GET", srv.URL+"/api/agent/tasks", nil, "", tok)
	if code != http.StatusOK {
		t.Fatalf("拉取任务应 200，实际 %d: %s", code, body)
	}
	return mustJSON(t, body)["tasks"].([]any)
}

func TestTaskLifecycle(t *testing.T) {
	s, srv := newTestServer(t)
	a, tok := newAgent(t, s, "worker-1")
	sess := adminSession(s)

	if got := pullTasks(t, srv, tok); len(got) != 0 {
		t.Fatalf("新 Agent 应无任务，实际 %d 个", len(got))
	}

	taskID := createTask(t, srv, sess, a.ID)

	got := pullTasks(t, srv, tok)
	if len(got) != 1 {
		t.Fatalf("应拉到 1 个任务，实际 %d", len(got))
	}
	item := got[0].(map[string]any)
	assignID := item["assignment_id"].(string)
	if !strings.HasPrefix(assignID, "tsa_") || item["task_id"] != taskID {
		t.Fatalf("拉取返回内容异常: %v", item)
	}
	if item["content"] != "检查磁盘占用并汇报" {
		t.Fatalf("任务内容不符: %v", item["content"])
	}

	// 幂等：再拉为空（拉取即投递，不会重复下发）
	if again := pullTasks(t, srv, tok); len(again) != 0 {
		t.Fatalf("重复拉取应为空，实际 %d 个", len(again))
	}

	// 回写成功
	code, body := doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+assignID+"/result",
		map[string]any{"status": "done", "result": "磁盘占用 42%"}, "", tok)
	if code != http.StatusOK {
		t.Fatalf("回写应 200，实际 %d: %s", code, body)
	}

	// 重复回写 409
	code, _ = doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+assignID+"/result",
		map[string]any{"status": "done", "result": "再来一次"}, "", tok)
	if code != http.StatusConflict {
		t.Fatalf("重复回写应 409，实际 %d", code)
	}

	// 详情：聚合状态 done，结果可见
	code, body = doJSON(t, "GET", srv.URL+"/api/tasks/"+taskID, nil, sess, "")
	if code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d", code)
	}
	detail := mustJSON(t, body)
	if detail["status"] != "done" {
		t.Fatalf("聚合状态应为 done，实际 %v", detail["status"])
	}
	as := detail["assignments"].([]any)
	if as[0].(map[string]any)["result"] != "磁盘占用 42%" {
		t.Fatalf("结果未写回: %v", as[0])
	}

	// 已结束的任务不可取消
	code, _ = doJSON(t, "POST", srv.URL+"/api/tasks/"+taskID+"/cancel", nil, sess, "")
	if code != http.StatusConflict {
		t.Fatalf("取消已完成任务应 409，实际 %d", code)
	}
}

func TestTaskMultiAgentIsolation(t *testing.T) {
	s, srv := newTestServer(t)
	a1, tok1 := newAgent(t, s, "worker-1")
	a2, tok2 := newAgent(t, s, "worker-2")
	sess := adminSession(s)

	taskID := createTask(t, srv, sess, a1.ID, a2.ID)

	got1, got2 := pullTasks(t, srv, tok1), pullTasks(t, srv, tok2)
	if len(got1) != 1 || len(got2) != 1 {
		t.Fatalf("两个 Agent 应各得 1 个任务: %d / %d", len(got1), len(got2))
	}
	as1 := got1[0].(map[string]any)["assignment_id"].(string)
	as2 := got2[0].(map[string]any)["assignment_id"].(string)
	if as1 == as2 {
		t.Fatal("同一任务的多个指派应有不同 assignment_id")
	}

	// 越权：Agent2 不能回写 Agent1 的指派
	code, _ := doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+as1+"/result",
		map[string]any{"status": "done", "result": "x"}, "", tok2)
	if code != http.StatusNotFound {
		t.Fatalf("越权回写应 404，实际 %d", code)
	}

	doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+as1+"/result",
		map[string]any{"status": "done", "result": "A 完成"}, "", tok1)
	doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+as2+"/result",
		map[string]any{"status": "failed", "result": "B 失败"}, "", tok2)

	_, body := doJSON(t, "GET", srv.URL+"/api/tasks/"+taskID, nil, sess, "")
	if st := mustJSON(t, body)["status"]; st != "partial" {
		t.Fatalf("一成一败应聚合为 partial，实际 %v", st)
	}
}

func TestTaskCancel(t *testing.T) {
	s, srv := newTestServer(t)
	a, tok := newAgent(t, s, "worker-1")
	sess := adminSession(s)
	taskID := createTask(t, srv, sess, a.ID)

	code, body := doJSON(t, "POST", srv.URL+"/api/tasks/"+taskID+"/cancel", nil, sess, "")
	if code != http.StatusOK {
		t.Fatalf("取消应 200，实际 %d: %s", code, body)
	}
	// 取消后拉不到
	if got := pullTasks(t, srv, tok); len(got) != 0 {
		t.Fatalf("取消后应拉不到任务，实际 %d 个", len(got))
	}
	// 取消后列表状态
	_, body = doJSON(t, "GET", srv.URL+"/api/tasks", nil, sess, "")
	tasks := mustJSON(t, body)["tasks"].([]any)
	if tasks[0].(map[string]any)["status"] != "canceled" {
		t.Fatalf("任务状态应为 canceled，实际 %v", tasks[0].(map[string]any)["status"])
	}
	// 重复取消 409
	code, _ = doJSON(t, "POST", srv.URL+"/api/tasks/"+taskID+"/cancel", nil, sess, "")
	if code != http.StatusConflict {
		t.Fatalf("重复取消应 409，实际 %d", code)
	}
}

func TestTaskRequeue(t *testing.T) {
	s, srv := newTestServer(t)
	a, tok := newAgent(t, s, "worker-1")
	sess := adminSession(s)
	createTask(t, srv, sess, a.ID)

	got := pullTasks(t, srv, tok)
	assignID := got[0].(map[string]any)["assignment_id"].(string)

	// 重新投递后可再次拉到
	code, body := doJSON(t, "POST", srv.URL+"/api/assignments/"+assignID+"/requeue", nil, sess, "")
	if code != http.StatusOK {
		t.Fatalf("重新投递应 200，实际 %d: %s", code, body)
	}
	again := pullTasks(t, srv, tok)
	if len(again) != 1 || again[0].(map[string]any)["assignment_id"] != assignID {
		t.Fatalf("重新投递后应拉到同一指派: %v", again)
	}

	// 写回结果后不可再重新投递
	doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+assignID+"/result",
		map[string]any{"status": "done", "result": "OK"}, "", tok)
	code, _ = doJSON(t, "POST", srv.URL+"/api/assignments/"+assignID+"/requeue", nil, sess, "")
	if code != http.StatusConflict {
		t.Fatalf("已完成的指派重新投递应 409，实际 %d", code)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	s, srv := newTestServer(t)
	a, _ := newAgent(t, s, "worker-1")
	sess := adminSession(s)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"空标题", map[string]any{"title": "", "content": "x", "agent_ids": []string{a.ID}}},
		{"空内容", map[string]any{"title": "x", "content": "  ", "agent_ids": []string{a.ID}}},
		{"无接收者", map[string]any{"title": "x", "content": "x", "agent_ids": []string{}}},
		{"Agent 不存在", map[string]any{"title": "x", "content": "x", "agent_ids": []string{"am_nonexist"}}},
	}
	for _, c := range cases {
		code, _ := doJSON(t, "POST", srv.URL+"/api/tasks", c.body, sess, "")
		if code != http.StatusBadRequest {
			t.Fatalf("%s：应 400", c.name)
		}
	}

	// 超过 20 个接收者
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = "am_fake" + strings.Repeat("0", i) + "x"
	}
	code, _ := doJSON(t, "POST", srv.URL+"/api/tasks",
		map[string]any{"title": "x", "content": "x", "agent_ids": ids}, sess, "")
	if code != http.StatusBadRequest {
		t.Fatalf("超过 20 个接收者应 400，实际 %d", code)
	}

	// 未登录 401
	code, _ = doJSON(t, "POST", srv.URL+"/api/tasks",
		map[string]any{"title": "x", "content": "x", "agent_ids": []string{a.ID}}, "", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("未登录创建任务应 401，实际 %d", code)
	}
}

func TestAgentTaskAuth(t *testing.T) {
	s, srv := newTestServer(t)
	a, tok := newAgent(t, s, "worker-1")
	sess := adminSession(s)
	createTask(t, srv, sess, a.ID)

	// 无令牌 / 错误前缀令牌
	if code, _ := doJSON(t, "GET", srv.URL+"/api/agent/tasks", nil, "", ""); code != http.StatusUnauthorized {
		t.Fatalf("无令牌拉取应 401，实际 %d", code)
	}
	if code, _ := doJSON(t, "GET", srv.URL+"/api/agent/tasks", nil, "", "ame_xxx"); code != http.StatusUnauthorized {
		t.Fatalf("注册令牌不能拉任务，应 401，实际 %d", code)
	}
	if code, _ := doJSON(t, "GET", srv.URL+"/api/agent/tasks", nil, "", "amh_wrong"); code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401，实际 %d", code)
	}

	got := pullTasks(t, srv, tok)
	assignID := got[0].(map[string]any)["assignment_id"].(string)
	if code, _ := doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+assignID+"/result",
		map[string]any{"status": "done", "result": "x"}, "", ""); code != http.StatusUnauthorized {
		t.Fatalf("无令牌回写应 401，实际 %d", code)
	}

	// 非法 status
	code, _ := doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+assignID+"/result",
		map[string]any{"status": "ok", "result": "x"}, "", tok)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 status 应 400，实际 %d", code)
	}
}

func TestDeleteAgentCancelsAssignments(t *testing.T) {
	s, srv := newTestServer(t)
	a, tok := newAgent(t, s, "worker-1")
	sess := adminSession(s)
	taskID := createTask(t, srv, sess, a.ID)
	pullTasks(t, srv, tok) // 置为 delivered

	code, _ := doJSON(t, "DELETE", srv.URL+"/api/agents/"+a.ID, nil, sess, "")
	if code != http.StatusOK {
		t.Fatalf("删除 Agent 应 200，实际 %d", code)
	}

	_, body := doJSON(t, "GET", srv.URL+"/api/tasks/"+taskID, nil, sess, "")
	detail := mustJSON(t, body)
	as := detail["assignments"].([]any)[0].(map[string]any)
	if as["status"] != AsCanceled {
		t.Fatalf("删除 Agent 后未结束指派应为 canceled，实际 %v", as["status"])
	}
	if detail["status"] != "canceled" {
		t.Fatalf("全部指派被取消后聚合状态应为 canceled，实际 %v", detail["status"])
	}
}

// TestDecommission 下线流程：删除后该令牌心跳收到 410（触发自卸载），未知令牌仍 401。
func TestDecommission(t *testing.T) {
	s, srv := newTestServer(t)
	sess := adminSession(s)
	a, tok := newAgent(t, s, "bye-agent")

	code, _ := doJSON(t, "POST", srv.URL+"/api/heartbeat", nil, "", tok)
	if code != http.StatusOK {
		t.Fatalf("下线前心跳应 200，实际 %d", code)
	}

	code, _ = doJSON(t, "DELETE", srv.URL+"/api/agents/"+a.ID, nil, sess, "")
	if code != http.StatusOK {
		t.Fatalf("下线应 200，实际 %d", code)
	}

	code, body := doJSON(t, "POST", srv.URL+"/api/heartbeat", nil, "", tok)
	if code != http.StatusGone {
		t.Fatalf("下线后心跳应 410，实际 %d", code)
	}
	if v, _ := mustJSON(t, body)["uninstall"].(bool); !v {
		t.Fatalf("410 响应应带 uninstall:true，实际 %s", body)
	}

	code, _ = doJSON(t, "POST", srv.URL+"/api/heartbeat", nil, "", "amh_garbagegarbagegarbage")
	if code != http.StatusUnauthorized {
		t.Fatalf("未知令牌应 401，实际 %d", code)
	}

	code, _ = doJSON(t, "DELETE", srv.URL+"/api/agents/"+a.ID, nil, sess, "")
	if code != http.StatusNotFound {
		t.Fatalf("重复下线应 404，实际 %d", code)
	}
}
