// agent-matrix：轻量 Agent 注册与在线状态监控中心。
// 单二进制 + 嵌入式 SQLite + 内嵌 WebUI，无外部运行时依赖。
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed all:web
var webFS embed.FS

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	store, err := openStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer store.Close()

	sessionKey, err := store.sessionSecret()
	if err != nil {
		log.Fatalf("初始化会话密钥失败: %v", err)
	}

	s := &server{
		cfg:        cfg,
		store:      store,
		rl:         newRateLimiter(10, time.Minute),
		sessionKey: sessionKey,
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("嵌入资源错误: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/register", s.handleRegister)
	mux.HandleFunc("POST /api/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/agents", s.requireAdmin(s.handleListAgents))
	mux.HandleFunc("DELETE /api/agents/{id}", s.requireAdmin(s.handleDeleteAgent))
	mux.HandleFunc("POST /api/enrollments", s.requireAdmin(s.handleCreateEnrollment))
	mux.Handle("GET /", http.FileServerFS(static))

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("agent-matrix 已启动，监听 %s", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("正在优雅关闭...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("关闭异常: %v", err)
	}
}
