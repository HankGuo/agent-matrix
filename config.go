package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// config 服务运行配置，全部通过环境变量注入。
type config struct {
	Addr          string        // HTTP 监听地址
	DBPath        string        // SQLite 数据库文件路径
	BaseURL       string        // 平台对外访问地址（写入接入指令）
	AdminToken    string        // WebUI 管理口令
	OnlineTimeout time.Duration // 超过该时长无心跳则视为离线
}

func loadConfig() (*config, error) {
	cfg := &config{
		Addr:          envOr("AGENT_MATRIX_ADDR", ":8080"),
		DBPath:        envOr("AGENT_MATRIX_DB", "./agent-matrix.db"),
		BaseURL:       strings.TrimRight(envOr("AGENT_MATRIX_BASE_URL", "http://localhost:8080"), "/"),
		AdminToken:    os.Getenv("AGENT_MATRIX_ADMIN_TOKEN"),
		OnlineTimeout: 3 * time.Minute,
	}
	if v := os.Getenv("AGENT_MATRIX_ONLINE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("AGENT_MATRIX_ONLINE_TIMEOUT 无效: %q", v)
		}
		cfg.OnlineTimeout = d
	}
	if cfg.AdminToken == "" {
		log.Println("提示: 未设置 AGENT_MATRIX_ADMIN_TOKEN，首次访问 WebUI 将强制初始化管理员账号")
	} else if len(cfg.AdminToken) < 8 {
		log.Println("警告: AGENT_MATRIX_ADMIN_TOKEN 长度不足 8 位，建议使用更强的口令")
	}
	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
