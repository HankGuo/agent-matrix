package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// enrollAndRegister 造一枚注册令牌并走完整注册接口，返回 Agent ID 与心跳令牌。
func enrollAndRegister(t *testing.T, srv *httptest.Server, s *server, meta string) (agentID, hbToken string) {
	t.Helper()
	token, _, err := s.store.createEnrollment("t", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	code, body := doJSON(t, "POST", srv.URL+"/api/register", map[string]string{
		"token": token, "name": "bot", "hostname": "h1", "os": "Linux", "arch": "arm64", "meta": meta,
	}, "", "")
	if code != 201 {
		t.Fatalf("注册状态码 = %d, body = %s", code, body)
	}
	var resp struct {
		AgentID        string `json:"agent_id"`
		HeartbeatToken string `json:"heartbeat_token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.AgentID, resp.HeartbeatToken
}

// agentMeta 从管理端列表取指定 Agent 的 meta 文本。
func agentMeta(t *testing.T, srv *httptest.Server, s *server, id string) string {
	t.Helper()
	code, body := doJSON(t, "GET", srv.URL+"/api/agents", nil, adminSession(s), "")
	if code != 200 {
		t.Fatalf("查询 Agent 列表状态码 = %d", code)
	}
	var list struct {
		Agents []Agent `json:"agents"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	for _, a := range list.Agents {
		if a.ID == id {
			return a.Meta
		}
	}
	t.Fatalf("列表中未找到 Agent %s", id)
	return ""
}

// TestRegisterWithMeta 验证注册携带能力画像：合法 meta 落库并随列表返回。
func TestRegisterWithMeta(t *testing.T) {
	s, srv := newTestServer(t)
	meta := `{"persona":"Go 后端与数据库运维","executor":"hermes","executor_version":"0.18.0","model":"anthropic/claude-sonnet-4","skills":["code","review"]}`
	id, _ := enrollAndRegister(t, srv, s, meta)
	got := agentMeta(t, srv, s, id)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("落库 meta 不是合法 JSON: %v", err)
	}
	if m["persona"] != "Go 后端与数据库运维" || m["executor"] != "hermes" {
		t.Errorf("meta 内容不符: %s", got)
	}
	if sk, ok := m["skills"].([]any); !ok || len(sk) != 2 {
		t.Errorf("skills 应为 2 项数组: %s", got)
	}
}

// TestRegisterMetaValidation 验证非法 meta 被拒：非 JSON 对象文本、超长。
func TestRegisterMetaValidation(t *testing.T) {
	s, srv := newTestServer(t)
	for _, meta := range []string{`"just-a-string"`, `[1,2,3]`, `{broken`, strings.Repeat("x", 2049)} {
		token, _, err := s.store.createEnrollment("t", 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		code, _ := doJSON(t, "POST", srv.URL+"/api/register", map[string]string{
			"token": token, "name": "bot", "meta": meta,
		}, "", "")
		if code != 400 {
			t.Errorf("meta %q 应返回 400，实际 %d", meta[:min(20, len(meta))], code)
		}
	}
}

// TestHeartbeatMetaRefresh 验证心跳刷新画像：带 meta 覆盖更新，不带则保留原值。
func TestHeartbeatMetaRefresh(t *testing.T) {
	s, srv := newTestServer(t)
	id, hb := enrollAndRegister(t, srv, s, `{"executor":"hermes","model":"m1"}`)

	code, _ := doJSON(t, "POST", srv.URL+"/api/heartbeat", map[string]string{
		"meta": `{"executor":"hermes","executor_version":"0.19.0","model":"m2"}`,
	}, "", hb)
	if code != 200 {
		t.Fatalf("带 meta 心跳状态码 = %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(agentMeta(t, srv, s, id)), &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "m2" || m["executor_version"] != "0.19.0" {
		t.Errorf("心跳未刷新 meta: %v", m)
	}

	// 不带 meta 的常规心跳：meta 保持不动
	code, _ = doJSON(t, "POST", srv.URL+"/api/heartbeat", nil, "", hb)
	if code != 200 {
		t.Fatalf("常规心跳状态码 = %d", code)
	}
	if got := agentMeta(t, srv, s, id); !strings.Contains(got, "m2") {
		t.Errorf("常规心跳不应覆盖 meta，实际: %s", got)
	}
}
