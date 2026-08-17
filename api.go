package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// heartbeatInterval 是建议的心跳间隔（秒），随注册响应下发。
const heartbeatInterval = 60

type server struct {
	cfg   *config
	store *store
	rl    *rateLimiter
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().Unix()})
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

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.rl.allow("login:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.cfg.AdminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "口令错误")
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
		"prompt":     buildPrompt(s.cfg.BaseURL, label, token),
	})
}
