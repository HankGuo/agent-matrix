package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	Storage       string        // 附件存储驱动：local（S3 为预留扩展点）
	AttachDir     string        // local 驱动的附件目录
}

func loadConfig() (*config, error) {
	dbPath := envOr("AGENT_MATRIX_DB", "./agent-matrix.db")
	cfg := &config{
		Addr:          envOr("AGENT_MATRIX_ADDR", ":26817"),
		DBPath:        dbPath,
		BaseURL:       strings.TrimRight(envOr("AGENT_MATRIX_BASE_URL", "http://localhost:26817"), "/"),
		AdminToken:    os.Getenv("AGENT_MATRIX_ADMIN_TOKEN"),
		OnlineTimeout: 3 * time.Minute,
		Storage:       envOr("AGENT_MATRIX_STORAGE", "local"),
		AttachDir:     envOr("AGENT_MATRIX_ATTACH_DIR", filepath.Join(filepath.Dir(dbPath), "attachments")),
	}
	if cfg.Storage != "local" {
		return nil, fmt.Errorf("AGENT_MATRIX_STORAGE=%q 尚未支持：当前仅实现 local（S3 预签名驱动是预留扩展点）", cfg.Storage)
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
