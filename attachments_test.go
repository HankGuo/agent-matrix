package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// doMultipart 以 multipart 表单发请求，fields 为文本字段，files 为 (desc, filename, content) 三元组。
func doMultipart(t *testing.T, url string, fields map[string]string, files [][3]string, session, bearer string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		if err := w.WriteField("desc", f[0]); err != nil {
			t.Fatal(err)
		}
		fw, err := w.CreateFormFile("file", f[1])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(f[2])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
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

// TestAttachmentLifecycle 覆盖附件全链路：multipart 建任务 → 拉取带清单 → 鉴权下载 →
// 产出上传（delivered 限定）→ 详情聚合 → 删除任务级联清理。
func TestAttachmentLifecycle(t *testing.T) {
	s, srv := newTestServer(t)
	sess := adminSession(s)
	ag, tok := newAgent(t, s, "att-agent")
	other, otherTok := newAgent(t, s, "att-other")
	_ = other

	// 1. multipart 创建任务：两个附件 + 各自说明
	code, body := doMultipart(t, srv.URL+"/api/tasks",
		map[string]string{"title": "分析数据", "content": "对比附件1和附件2", "agent_ids": ag.ID},
		[][3]string{
			{"Q4 销售明细", "sales.xlsx", "fake-xlsx-bytes-12345"},
			{"会议录音", "meeting.mp3", "fake-audio"},
		}, sess, "")
	if code != http.StatusCreated {
		t.Fatalf("multipart 建任务应 201，实际 %d: %s", code, body)
	}
	taskID := mustJSON(t, body)["task"].(map[string]any)["id"].(string)

	// 2. 拉取：附件清单带下载 URL 与描述
	code, body = doJSON(t, "GET", srv.URL+"/api/agent/tasks", nil, "", tok)
	if code != http.StatusOK {
		t.Fatalf("拉取应 200，实际 %d", code)
	}
	pulled := mustJSON(t, body)["tasks"].([]any)
	if len(pulled) != 1 {
		t.Fatalf("应拉到 1 个任务，实际 %d", len(pulled))
	}
	pt := pulled[0].(map[string]any)
	atts := pt["attachments"].([]any)
	if len(atts) != 2 {
		t.Fatalf("应有 2 个附件，实际 %d", len(atts))
	}
	a0 := atts[0].(map[string]any)
	if a0["name"] != "sales.xlsx" || a0["description"] != "Q4 销售明细" {
		t.Fatalf("附件元数据不对: %v", a0)
	}
	attID := a0["id"].(string)
	assignID := pt["assignment_id"].(string)

	// 3. 下载鉴权：本人 200、内容一致；其他 Agent 403；无令牌 401
	code, body = doJSON(t, "GET", srv.URL+a0["url"].(string), nil, "", tok)
	if code != http.StatusOK || string(body) != "fake-xlsx-bytes-12345" {
		t.Fatalf("本人下载应 200 且内容一致，实际 %d %q", code, body)
	}
	code, _ = doJSON(t, "GET", srv.URL+"/api/agent/attachments/"+attID, nil, "", otherTok)
	if code != http.StatusForbidden {
		t.Fatalf("他人下载应 403，实际 %d", code)
	}
	code, _ = doJSON(t, "GET", srv.URL+"/api/agent/attachments/"+attID, nil, "", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("无令牌下载应 401，实际 %d", code)
	}

	// 4. 产出上传：delivered 状态可传；产出对其他人不可见
	code, body = doMultipart(t, srv.URL+"/api/agent/tasks/"+assignID+"/outputs",
		nil, [][3]string{{"", "report.md", "# 结论\n……"}}, "", tok)
	if code != http.StatusCreated {
		t.Fatalf("产出上传应 201，实际 %d: %s", code, body)
	}
	outID := mustJSON(t, body)["attachment"].(map[string]any)["id"].(string)
	code, _ = doJSON(t, "GET", srv.URL+"/api/agent/attachments/"+outID, nil, "", tok)
	if code != http.StatusForbidden {
		t.Fatalf("产出件对 Agent 下载应 403，实际 %d", code)
	}

	// 5. 回写后再传产出应 409
	code, _ = doJSON(t, "POST", srv.URL+"/api/agent/tasks/"+assignID+"/result",
		map[string]string{"status": "done", "result": "ok"}, "", tok)
	if code != http.StatusOK {
		t.Fatalf("回写应 200，实际 %d", code)
	}
	code, _ = doMultipart(t, srv.URL+"/api/agent/tasks/"+assignID+"/outputs",
		nil, [][3]string{{"", "late.txt", "too late"}}, "", tok)
	if code != http.StatusConflict {
		t.Fatalf("回写后上传产出应 409，实际 %d", code)
	}

	// 6. 详情：输入件与产出件就位
	code, body = doJSON(t, "GET", srv.URL+"/api/tasks/"+taskID, nil, sess, "")
	if code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d", code)
	}
	detail := mustJSON(t, body)
	if len(detail["inputs"].([]any)) != 2 {
		t.Fatalf("详情应有 2 个输入件")
	}
	outs := detail["outputs"].(map[string]any)[assignID].([]any)
	if len(outs) != 1 || outs[0].(map[string]any)["name"] != "report.md" {
		t.Fatalf("详情产出件不对: %v", outs)
	}

	// 7. 管理员下载产出件 200
	code, body = doJSON(t, "GET", srv.URL+"/api/attachments/"+outID, nil, sess, "")
	if code != http.StatusOK || !strings.Contains(string(body), "结论") {
		t.Fatalf("管理员下载产出件应 200，实际 %d", code)
	}

	// 8. 删除任务：记录与文件级联清理
	code, _ = doJSON(t, "POST", srv.URL+"/api/tasks/"+taskID+"/delete", map[string]bool{}, sess, "")
	if code != http.StatusOK {
		t.Fatalf("删除任务应 200，实际 %d", code)
	}
	code, _ = doJSON(t, "GET", srv.URL+"/api/attachments/"+attID, nil, sess, "")
	if code != http.StatusNotFound {
		t.Fatalf("删除后附件应 404，实际 %d", code)
	}
	code, _ = doJSON(t, "GET", srv.URL+"/api/tasks/"+taskID, nil, sess, "")
	if code != http.StatusNotFound {
		t.Fatalf("删除后任务应 404，实际 %d", code)
	}
}

// TestAttachmentMimePolicy 验证 MIME 嗅探与 inline 白名单：客户端声明不可信。
func TestAttachmentMimePolicy(t *testing.T) {
	s, srv := newTestServer(t)
	sess := adminSession(s)
	ag, _ := newAgent(t, s, "mime-agent")

	// HTML 伪装成图片上传：嗅探应识别为 text/html，下载响应必须强制 attachment
	code, body := doMultipart(t, srv.URL+"/api/tasks",
		map[string]string{"title": "t", "content": "c", "agent_ids": ag.ID},
		[][3]string{{"", "evil.png", "<html><script>alert(1)</script></html>"}}, sess, "")
	if code != http.StatusCreated {
		t.Fatalf("建任务应 201，实际 %d: %s", code, body)
	}
	taskID := mustJSON(t, body)["task"].(map[string]any)["id"].(string)
	code, body = doJSON(t, "GET", srv.URL+"/api/tasks/"+taskID, nil, sess, "")
	att := mustJSON(t, body)["inputs"].([]any)[0].(map[string]any)
	if !strings.HasPrefix(att["mime"].(string), "text/html") {
		t.Fatalf("应嗅探为 text/html，实际 %s", att["mime"])
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/attachments/"+att["id"].(string), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if cd := res.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("HTML 内容必须强制下载，实际 Content-Disposition: %q", cd)
	}

	// 白名单单测
	for m, want := range map[string]bool{
		"image/png": true, "image/svg+xml": false, "audio/mpeg": true,
		"video/mp4": true, "application/pdf": true, "text/html": false,
		"application/zip": false,
	} {
		if got := inlinePreviewable(m); got != want {
			t.Errorf("inlinePreviewable(%q) = %v, want %v", m, got, want)
		}
	}
}
