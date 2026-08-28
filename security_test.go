package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestClientIPTrust 验证 X-Forwarded-For 只在可信代理场景被采信，
// 且取最右一段（客户端自带的伪造段留在左侧）——否则直接暴露公网时
// 攻击者轮换伪造 IP 即可绕过所有限流。
func TestClientIPTrust(t *testing.T) {
	public := "192.0.2.1:1234" // TEST-NET，非回环非内网
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = public
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 10.0.0.1")

	// auto：对端非内网 → 不信任 XFF，用 TCP 对端地址
	s := &server{cfg: &config{TrustProxy: "auto"}}
	if got := s.clientIP(req); got != "192.0.2.1" {
		t.Fatalf("auto + 公网对端应忽略 XFF，得到 %q", got)
	}
	if s.requestIsHTTPS(req) {
		t.Fatal("auto + 公网对端不应采信 X-Forwarded-Proto")
	}

	// auto：对端是回环（同机反代）→ 采信 XFF 最右段
	req.RemoteAddr = "127.0.0.1:5555"
	if got := s.clientIP(req); got != "10.0.0.1" {
		t.Fatalf("auto + 回环对端应取 XFF 最右段，得到 %q", got)
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	if !s.requestIsHTTPS(req) {
		t.Fatal("可信代理声明 https 时 requestIsHTTPS 应为真")
	}

	// auto：对端是内网地址（容器化反代）→ 采信 XFF
	req.RemoteAddr = "172.17.0.5:40000"
	req.Header.Del("X-Forwarded-Proto")
	if got := s.clientIP(req); got != "10.0.0.1" {
		t.Fatalf("auto + 内网对端应取 XFF 最右段，得到 %q", got)
	}

	// false：一律不信任；true：一律信任
	sf := &server{cfg: &config{TrustProxy: "false"}}
	req.RemoteAddr = "127.0.0.1:5555"
	if got := sf.clientIP(req); got != "127.0.0.1" {
		t.Fatalf("false 应始终用 TCP 对端地址，得到 %q", got)
	}
	st := &server{cfg: &config{TrustProxy: "true"}}
	req.RemoteAddr = public
	if got := st.clientIP(req); got != "10.0.0.1" {
		t.Fatalf("true 应采信 XFF 最右段，得到 %q", got)
	}

	// 无逗号的单段 XFF 也要能取到
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	if got := st.clientIP(req); got != "8.8.8.8" {
		t.Fatalf("单段 XFF 应直接采信，得到 %q", got)
	}
}

// TestXFFCannotBypassLoginLimit HTTP 层验证：直连公网（TrustProxy=false）
// 时轮换伪造 XFF 无法绕过登录限流；显式信任代理时限流按 XFF 分桶。
func TestXFFCannotBypassLoginLimit(t *testing.T) {
	s, srv := newTestServer(t)
	code, _ := doJSON(t, "POST", srv.URL+"/api/setup",
		map[string]string{"username": "admin", "password": "password123"}, "", "")
	if code != 201 {
		t.Fatalf("初始化状态码 = %d", code)
	}
	s.rl = newRateLimiter(3, time.Minute)
	postLogin := func(xff string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/login",
			strings.NewReader(`{"username":"admin","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	// httptest 服务对端是 127.0.0.1（可信），显式关掉信任模拟公网直连
	s.cfg.TrustProxy = "false"
	for i := 0; i < 3; i++ {
		if c := postLogin("1.2.3." + string(rune('a'+i))); c != 401 {
			t.Fatalf("第 %d 次伪造 XFF 登录应 401，实际 %d", i+1, c)
		}
	}
	if c := postLogin("1.2.3.zzz"); c != 429 {
		t.Fatalf("轮换伪造 XFF 第 4 次应被限流 429，实际 %d", c)
	}
	// 信任代理时：不同 XFF 是不同桶，各自 401 而非 429
	s.cfg.TrustProxy = "true"
	s.rl = newRateLimiter(3, time.Minute)
	for i := 0; i < 4; i++ {
		if c := postLogin("9.9.9." + string(rune('a'+i))); c != 401 {
			t.Fatalf("信任代理时第 %d 个伪造 IP 应 401，实际 %d", i+1, c)
		}
	}
}

// TestLogoutInvalidatesSession 验证会话撤销：logout 后旧 Cookie 立即失效。
func TestLogoutInvalidatesSession(t *testing.T) {
	s, srv := newTestServer(t)
	code, _ := doJSON(t, "POST", srv.URL+"/api/setup",
		map[string]string{"username": "admin", "password": "password123"}, "", "")
	if code != 201 {
		t.Fatalf("初始化状态码 = %d", code)
	}
	sess := s.sessionValue(time.Now().Add(time.Hour).Unix())
	if code, _ := doJSON(t, "GET", srv.URL+"/api/agents", nil, sess, ""); code != 200 {
		t.Fatalf("登出前会话应有效，状态码 %d", code)
	}
	if code, _ := doJSON(t, "POST", srv.URL+"/api/logout", map[string]string{}, sess, ""); code != 200 {
		t.Fatalf("登出状态码 = %d", code)
	}
	if code, _ := doJSON(t, "GET", srv.URL+"/api/agents", nil, sess, ""); code != 401 {
		t.Fatalf("登出后旧会话应立即失效（401），实际 %d", code)
	}
	// 纪元更新后新登录的会话有效
	sess2 := s.sessionValue(time.Now().Add(time.Hour).Unix())
	if code, _ := doJSON(t, "GET", srv.URL+"/api/agents", nil, sess2, ""); code != 200 {
		t.Fatalf("新纪元会话应有效，状态码 %d", code)
	}
}

// TestDBFilePerm0600 验证库文件以 0600 创建，且存量过宽权限文件启动时被收紧。
func TestDBFilePerm0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX 权限位在 Windows 上无意义")
	}
	dir := t.TempDir()

	// 新建即 0600
	p1 := filepath.Join(dir, "new.db")
	st, err := openStore(p1)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if info, err := os.Stat(p1); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("新建库文件权限 = %v，期望 -rw-------", info.Mode().Perm())
	}

	// 存量 0644 收紧为 0600（修复已上线的旧实例）
	p2 := filepath.Join(dir, "legacy.db")
	if err := os.WriteFile(p2, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := openStore(p2)
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
	if info, err := os.Stat(p2); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("存量库文件权限未被收紧 = %v", info.Mode().Perm())
	}
}

// TestHeartbeatChunkedMeta 验证 chunked 传输的心跳同样携带 meta
// （ContentLength = -1，旧实现用 > 0 判定会把 meta 静默丢弃）。
func TestHeartbeatChunkedMeta(t *testing.T) {
	s, srv := newTestServer(t)
	id, hb := enrollAndRegister(t, srv, s, "") // 注册时不带画像

	req, err := http.NewRequest("POST", srv.URL+"/api/heartbeat",
		io.NopCloser(strings.NewReader(`{"meta":"{\"chunked\":\"yes\"}"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+hb)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1 // 强制 chunked
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("chunked 心跳状态码 = %d", res.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(agentMeta(t, srv, s, id)), &m); err != nil {
		t.Fatal(err)
	}
	if m["chunked"] != "yes" {
		t.Fatalf("chunked 传输的 meta 未生效: %v", m)
	}
}

// TestBaseURLValidation 验证平台地址拒绝会注入提示词/脚本的字符。
func TestBaseURLValidation(t *testing.T) {
	for _, bad := range []string{
		"", "example.com", "ftp://x", "http://x.com/a b",
		`http://x.com/"onmouseover="`, "http://x.com/`id`", "http://x.com/a;ls",
		"http://x.com/a\tb", "http://x.com/$PATH", "http://user:pass@x.com/",
		"http://x.com/'quote'", "http://x.com/<script>",
	} {
		if _, err := normalizeBaseURL(bad); err == nil {
			t.Errorf("base_url %q 应被拒绝", bad)
		}
	}
	for _, good := range []string{
		"http://localhost:26817", "https://matrix.example.com",
		"https://matrix.example.com/base/path",
	} {
		if _, err := normalizeBaseURL(good); err != nil {
			t.Errorf("base_url %q 应通过: %v", good, err)
		}
	}
}

// TestRegisterInvalidTokenVsNameConflict 验证原子注册的错误语义：
// 无效令牌返回 401（不泄露名称占用信息）；有效令牌撞名返回 409 且令牌仍可重试。
func TestRegisterInvalidTokenVsNameConflict(t *testing.T) {
	s, srv := newTestServer(t)
	enrollAndRegister(t, srv, s, "") // 占用 "bot"

	// 无效令牌 + 已占用名称 → 401（不是 409）
	code, _ := doJSON(t, "POST", srv.URL+"/api/register",
		map[string]string{"token": "ame_bogus", "name": "bot"}, "", "")
	if code != 401 {
		t.Fatalf("无效令牌撞已占用名称应 401，实际 %d", code)
	}

	// 有效令牌 + 已占用名称 → 409，令牌未核销
	token, _, err := s.store.createEnrollment("t", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	code, _ = doJSON(t, "POST", srv.URL+"/api/register",
		map[string]string{"token": token, "name": "bot"}, "", "")
	if code != 409 {
		t.Fatalf("有效令牌撞名应 409，实际 %d", code)
	}
	if code, _ := doJSON(t, "POST", srv.URL+"/api/register",
		map[string]string{"token": token, "name": "bot-ok"}, "", ""); code != 201 {
		t.Fatalf("撞名后令牌应仍可用，实际 %d", code)
	}
}
