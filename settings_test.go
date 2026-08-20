package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// heartbeatPollInterval 发起一次心跳，返回响应里的 poll_interval。
func heartbeatPollInterval(t *testing.T, srv *httptest.Server, hbToken string) int {
	t.Helper()
	code, body := doJSON(t, "POST", srv.URL+"/api/heartbeat", nil, "", hbToken)
	if code != 200 {
		t.Fatalf("心跳状态码 = %d", code)
	}
	var resp struct {
		PollInterval int `json:"poll_interval"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.PollInterval
}

// TestPollIntervalSetting 验证全局轮询间隔：默认值、非法值拒绝、合法值保存读回、
// 心跳响应跟随设置变化（Agent 侧据此机械调整本机定时任务）。
func TestPollIntervalSetting(t *testing.T) {
	s, srv := newTestServer(t)
	sess := adminSession(s)
	_, hb := enrollAndRegister(t, srv, s, "")

	// 未配置时默认 60，随心跳下发
	if got := heartbeatPollInterval(t, srv, hb); got != defaultPollInterval {
		t.Fatalf("默认 poll_interval = %d，期望 %d", got, defaultPollInterval)
	}

	// 非法值：越界、非数字，一律 400
	for _, v := range []any{0, 9, 3601, -5, "abc"} {
		code, _ := doJSON(t, "POST", srv.URL+"/api/settings",
			map[string]any{"base_url": "http://test.local", "poll_interval": v}, sess, "")
		if code != 400 {
			t.Errorf("poll_interval=%v 应返回 400，实际 %d", v, code)
		}
	}

	// 合法值保存后，GET 与心跳响应都跟进
	code, _ := doJSON(t, "POST", srv.URL+"/api/settings",
		map[string]any{"base_url": "http://test.local", "poll_interval": 30}, sess, "")
	if code != 200 {
		t.Fatalf("保存 poll_interval=30 状态码 = %d", code)
	}
	code, body := doJSON(t, "GET", srv.URL+"/api/settings", nil, sess, "")
	if code != 200 {
		t.Fatalf("读取设置状态码 = %d", code)
	}
	var settings struct {
		PollInterval int `json:"poll_interval"`
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.PollInterval != 30 {
		t.Errorf("GET 设置 poll_interval = %d，期望 30", settings.PollInterval)
	}
	if got := heartbeatPollInterval(t, srv, hb); got != 30 {
		t.Errorf("心跳下发 poll_interval = %d，期望 30", got)
	}
}

// TestSettingsUnauthorized 验证设置接口需要管理员会话。
func TestSettingsUnauthorized(t *testing.T) {
	_, srv := newTestServer(t)
	if code, _ := doJSON(t, "GET", srv.URL+"/api/settings", nil, "", ""); code != 401 {
		t.Errorf("未登录 GET 设置状态码 = %d，期望 401", code)
	}
	if code, _ := doJSON(t, "POST", srv.URL+"/api/settings",
		map[string]any{"base_url": "http://test.local", "poll_interval": 30}, "", ""); code != 401 {
		t.Errorf("未登录 POST 设置状态码 = %d，期望 401", code)
	}
}
