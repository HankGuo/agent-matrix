package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// heartbeatInterval 是建议的心跳间隔（秒），随注册响应下发。
const heartbeatInterval = 60

type server struct {
	cfg        *config
	store      *store
	rl         *rateLimiter // 登录/注册等敏感公开接口
	pullRl     *rateLimiter // Agent 拉取任务，阈值宽松
	sessionKey string       // 会话签名密钥，持久化在 settings 表
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return false
	}
	return true
}

// clientIP 优先取反代注入的 X-Forwarded-For 首段。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clean 去掉控制字符并限制长度。
func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().Unix(), "version": version})
}

// handleSetupScript 下发一键接入脚本。公开接口：脚本本身不含任何密钥，
// 令牌由 Agent 在执行时通过环境变量传入。
func (s *server) handleSetupScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, strings.ReplaceAll(setupScript, "{{BASE_URL}}", s.baseURL()))
}

// ---- 公开接口（Agent 侧） ----

func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.rl.allow("register:" + ip) {
		writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		return
	}
	var req struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Meta     string `json:"meta"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name, req.Hostname, req.OS, req.Arch = clean(req.Name, 64), clean(req.Hostname, 128), clean(req.OS, 32), clean(req.Arch, 32)
	if !strings.HasPrefix(req.Token, "ame_") || req.Name == "" {
		writeError(w, http.StatusBadRequest, "缺少有效的 token 或 name")
		return
	}
	if req.Meta != "" && (len(req.Meta) > 2048 || !validMeta(req.Meta)) {
		writeError(w, http.StatusBadRequest, "meta 必须是 2KB 以内的 JSON 对象")
		return
	}
	if _, err := s.store.consumeEnrollment(req.Token); err != nil {
		if errors.Is(err, errInvalidToken) {
			writeError(w, http.StatusUnauthorized, "注册令牌无效、已使用或已过期")
			return
		}
		log.Printf("核销注册令牌失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	a, raw, err := s.store.createAgent(req.Name, req.Hostname, req.OS, req.Arch, ip, req.Meta)
	if err != nil {
		log.Printf("创建 Agent 失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	log.Printf("Agent 注册成功: %s (%s) 来自 %s", a.Name, a.ID, ip)
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent_id":           a.ID,
		"heartbeat_token":    raw,
		"heartbeat_interval": heartbeatInterval,
		"server_time":        time.Now().Unix(),
	})
}

func (s *server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(token, "amh_") {
		writeError(w, http.StatusUnauthorized, "缺少心跳令牌")
		return
	}
	a, err := s.store.agentByToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "心跳令牌无效")
		return
	}
	var meta string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && r.ContentLength > 0 {
		var req struct {
			Meta string `json:"meta"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Meta != "" && (len(req.Meta) > 2048 || !validMeta(req.Meta)) {
			writeError(w, http.StatusBadRequest, "meta 必须是 2KB 以内的 JSON 对象")
			return
		}
		meta = req.Meta
	}
	if err := s.store.touchHeartbeat(a.ID, clientIP(r), meta); err != nil {
		log.Printf("更新心跳失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server_time": time.Now().Unix()})
}

// ---- 管理端接口 ----

// handleAuthStatus 向 WebUI 报告认证状态：是否需要初始化、是否启用令牌应急登录。
func (s *server) handleAuthStatus(w http.ResponseWriter, _ *http.Request) {
	has, err := s.store.hasAdmin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": !has,
		"env_login":   s.cfg.AdminToken != "",
		"base_url":    s.baseURL(),
		"version":     version,
	})
}

// baseURL 返回生效的平台对外地址：WebUI 设置优先，其次环境变量/默认值。
func (s *server) baseURL() string {
	if v, err := s.store.getSetting("base_url"); err == nil && v != "" {
		return v
	}
	return s.cfg.BaseURL
}

// normalizeBaseURL 校验并规范化平台地址。
func normalizeBaseURL(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return "", errors.New("平台地址不能为空")
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "", errors.New("平台地址必须以 http:// 或 https:// 开头")
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", errors.New("平台地址不是合法 URL")
	}
	return s, nil
}

// handleGetSettings 返回当前设置（管理端）。
func (s *server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url": s.baseURL(),
		"version":  version,
	})
}

// handleUpdateSettings 更新设置（管理端）。目前仅支持平台地址。
func (s *server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := normalizeBaseURL(req.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.setSetting("base_url", u); err != nil {
		log.Printf("保存设置失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	log.Printf("平台地址已更新: %s", u)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "base_url": u})
}

// handleSetup 首次访问初始化管理员账号，仅在无任何账号时可用。
func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.rl.allow("setup:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		return
	}
	has, err := s.store.hasAdmin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if has {
		writeError(w, http.StatusForbidden, "管理员账号已存在，请直接登录")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		BaseURL  string `json:"base_url"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = clean(req.Username, 32)
	if !validUsername(req.Username) {
		writeError(w, http.StatusBadRequest, "账号需 2-32 位，仅限字母、数字、_ . -")
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 128 {
		writeError(w, http.StatusBadRequest, "密码长度需 8-128 位")
		return
	}
	if req.BaseURL != "" {
		u, err := normalizeBaseURL(req.BaseURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.store.setSetting("base_url", u); err != nil {
			writeError(w, http.StatusInternalServerError, "内部错误")
			return
		}
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if err := s.store.createAdmin(req.Username, hash); err != nil {
		log.Printf("创建管理员失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	log.Printf("管理员账号已初始化: %q", req.Username)
	s.setSessionCookie(w, r)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.rl.allow("login:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"token"` // 应急通道：AGENT_MATRIX_ADMIN_TOKEN
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// 应急令牌通道（仅当配置了环境变量时开放）
	if req.Token != "" && s.cfg.AdminToken != "" &&
		subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.cfg.AdminToken)) == 1 {
		s.setSessionCookie(w, r)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	// 账号密码通道
	if req.Username != "" || req.Password != "" {
		hash, err := s.store.adminPasswordHash(clean(req.Username, 32))
		if err == nil && verifyPassword(req.Password, hash) {
			s.setSessionCookie(w, r)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
	}
	writeError(w, http.StatusUnauthorized, "账号或密码错误")
}

// setSessionCookie 种下 7 天有效的会话 Cookie。
func (s *server) setSessionCookie(w http.ResponseWriter, r *http.Request) {
	exp := time.Now().Add(sessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.sessionValue(exp),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		Expires:  time.Unix(exp, 0),
	})
}

// validUsername 限制账号字符集，避免界面注入与日志污染。
func validUsername(s string) bool {
	if len(s) < 2 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

func (s *server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleListAgents(w http.ResponseWriter, _ *http.Request) {
	agents, err := s.store.listAgents()
	if err != nil {
		log.Printf("查询 Agent 列表失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	type view struct {
		Agent
		Online bool `json:"online"`
	}
	now := time.Now().Unix()
	timeout := int64(s.cfg.OnlineTimeout.Seconds())
	out := make([]view, 0, len(agents))
	for _, a := range agents {
		out = append(out, view{Agent: a, Online: now-a.LastSeen <= timeout})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out, "online_timeout": timeout})
}

func (s *server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !strings.HasPrefix(id, "am_") {
		writeError(w, http.StatusBadRequest, "无效的 Agent ID")
		return
	}
	if err := s.store.cancelOpenAssignmentsForAgent(id); err != nil {
		log.Printf("取消 Agent 未结束指派失败 (%s): %v", id, err)
	}
	if err := s.store.deleteAgent(id); err != nil {
		log.Printf("删除 Agent 失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	log.Printf("Agent 已删除: %s", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) handleCreateEnrollment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	label := clean(req.Label, 64)
	token, exp, err := s.store.createEnrollment(label, 24*time.Hour)
	if err != nil {
		log.Printf("生成注册令牌失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	log.Printf("生成一次性注册令牌，备注: %q", label)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"expires_at": exp,
		"prompt":     buildPrompt(s.baseURL(), label, token),
	})
}
