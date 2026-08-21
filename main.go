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

// version 随发布手动递增，展示在 WebUI 页脚与 /healthz 中。
const version = "0.12.0"

//go:embed all:web
var webFS embed.FS

//go:embed setup.sh
var setupScript string

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

	blob, err := newLocalBlob(cfg.AttachDir)
	if err != nil {
		log.Fatalf("初始化附件目录失败: %v", err)
	}

	s := &server{
		cfg:        cfg,
		store:      store,
		blob:       blob,
		rl:         newRateLimiter(10, time.Minute),
		pullRl:     newRateLimiter(60, time.Minute),
		sessionKey: sessionKey,
		broker:     newSSEBroker(),
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute, // 放大以容纳大附件上传；慢速攻击由 ReadHeaderTimeout 兜底
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

// routes 注册全部路由并套上安全响应头，单独成方法便于测试复用。
func (s *server) routes() http.Handler {
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("嵌入资源错误: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /setup.sh", s.handleSetupScript)
	mux.HandleFunc("POST /api/register", s.handleRegister)
	mux.HandleFunc("POST /api/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("GET /api/events", s.requireAdmin(s.handleEvents))
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/agents", s.requireAdmin(s.handleListAgents))
	mux.HandleFunc("DELETE /api/agents/{id}", s.requireAdmin(s.handleDeleteAgent))
	mux.HandleFunc("POST /api/enrollments", s.requireAdmin(s.handleCreateEnrollment))
	mux.HandleFunc("GET /api/settings", s.requireAdmin(s.handleGetSettings))
	mux.HandleFunc("POST /api/settings", s.requireAdmin(s.handleUpdateSettings))
	// Agent 侧任务接口（Bearer amh_ 令牌）
	mux.HandleFunc("GET /api/agent/tasks", s.handlePullTasks)
	mux.HandleFunc("POST /api/agent/tasks/{id}/result", s.handleWriteResult)
	mux.HandleFunc("GET /api/agent/attachments/{id}", s.handleAgentGetAttachment)
	mux.HandleFunc("POST /api/agent/tasks/{id}/outputs", s.handleUploadOutput)
	// 管理端任务接口
	mux.HandleFunc("POST /api/tasks", s.requireAdmin(s.handleCreateTask))
	mux.HandleFunc("GET /api/tasks", s.requireAdmin(s.handleListTasks))
	mux.HandleFunc("GET /api/tasks/{id}", s.requireAdmin(s.handleTaskDetail))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.requireAdmin(s.handleCancelTask))
	mux.HandleFunc("POST /api/tasks/{id}/followup", s.requireAdmin(s.handleCreateFollowup))
	mux.HandleFunc("POST /api/tasks/{id}/delete", s.requireAdmin(s.handleDeleteTask))
	mux.HandleFunc("GET /api/attachments/{id}", s.requireAdmin(s.handleAdminGetAttachment))
	mux.HandleFunc("POST /api/assignments/{id}/requeue", s.requireAdmin(s.handleRequeueAssignment))
	mux.Handle("GET /", http.FileServerFS(static))
	return securityHeaders(mux)
}
