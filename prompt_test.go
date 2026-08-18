package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestSetupScriptRoute 验证 /setup.sh：200、占位符已替换为 baseURL、不含任何密钥。
func TestSetupScriptRoute(t *testing.T) {
	_, srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/setup.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "http://test.local") {
		t.Error("脚本未注入 baseURL")
	}
	if strings.Contains(s, "{{BASE_URL}}") {
		t.Error("占位符未被替换")
	}
	// 脚本文档注释里会出现 "ame_…" 这类占位写法，但不应出现真实的密钥值
	if strings.Contains(s, "amh_") || strings.Contains(s, "ame_-") {
		t.Error("脚本不应包含任何密钥")
	}
	if !strings.Contains(s, "AM_SETUP_DONE") {
		t.Error("脚本缺少自检汇报标记")
	}
}

// TestBuildPromptSlim 验证接入提示词已瘦身：只做引导，静态逻辑全部托管在 /setup.sh。
func TestBuildPromptSlim(t *testing.T) {
	p := buildPrompt("http://test.local", "my-agent", "ame_testtoken")
	for _, want := range []string{"/setup.sh", "ame_testtoken", "http://test.local", "my-agent"} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词缺少 %q", want)
		}
	}
	// 不应再内嵌安装脚本细节（提示词可以提到 cron/launchd 等名词，但不能出现脚本实现）
	for _, ban := range []string{"<<", "heartbeat_token", "LaunchAgents", "systemctl", "launchctl "} {
		if strings.Contains(p, ban) {
			t.Errorf("提示词不应包含安装细节 %q", ban)
		}
	}
	if lines := strings.Count(p, "\n") + 1; lines > 40 {
		t.Errorf("提示词过长: %d 行", lines)
	}

	up := buildTaskLoopPrompt("http://test.local")
	if !strings.Contains(up, "/setup.sh") || !strings.Contains(up, "http://test.local") {
		t.Error("升级指令应引导重新执行 setup.sh")
	}
}
