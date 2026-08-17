package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Agent 是一条已注册的 Agent 记录，时间字段均为 Unix 秒。
type Agent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	IP        string `json:"ip"`
	Meta      string `json:"meta"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
}

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写者，避免 database is locked
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS enroll_tokens (
    id         INTEGER PRIMARY KEY,
    token_hash TEXT UNIQUE NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);
CREATE TABLE IF NOT EXISTS agents (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    hostname   TEXT NOT NULL DEFAULT '',
    os         TEXT NOT NULL DEFAULT '',
    arch       TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    meta       TEXT NOT NULL DEFAULT '{}',
    token_hash TEXT UNIQUE NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS admin_credentials (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// ---- 管理员账号 ----

// hasAdmin 报告是否已初始化管理员账号。
func (s *store) hasAdmin() (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admin_credentials`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *store) createAdmin(username, passwordHash string) error {
	_, err := s.db.Exec(
		`INSERT INTO admin_credentials (id, username, password_hash, created_at) VALUES (1, ?, ?, ?)`,
		username, passwordHash, time.Now().Unix(),
	)
	return err
}

// adminPasswordHash 返回指定账号的密码哈希。
func (s *store) adminPasswordHash(username string) (string, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE username = ?`, username).Scan(&hash)
	return hash, err
}

// sessionSecret 返回持久的会话签名密钥，不存在则生成。
func (s *store) sessionSecret() (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = 'session_secret'`).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	v = randToken(32)
	_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES ('session_secret', ?)`, v)
	return v, err
}

var errInvalidToken = errors.New("令牌无效、已使用或已过期")

// createEnrollment 生成一次性注册令牌，明文令牌仅在创建时返回。
func (s *store) createEnrollment(label string, ttl time.Duration) (token string, expiresAt int64, err error) {
	token = "ame_" + randToken(24)
	expiresAt = time.Now().Add(ttl).Unix()
	_, err = s.db.Exec(
		`INSERT INTO enroll_tokens (token_hash, label, created_at, expires_at) VALUES (?,?,?,?)`,
		hashToken(token), label, time.Now().Unix(), expiresAt,
	)
	return token, expiresAt, err
}

// consumeEnrollment 校验并原子核销一次性令牌，返回其备注名。
func (s *store) consumeEnrollment(token string) (string, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(
		`UPDATE enroll_tokens SET used_at = ?
		 WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		now, hashToken(token), now,
	)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return "", errInvalidToken
	}
	var label string
	err = s.db.QueryRow(`SELECT label FROM enroll_tokens WHERE token_hash = ?`, hashToken(token)).Scan(&label)
	return label, err
}

// createAgent 创建 Agent 记录，返回记录与明文心跳令牌（仅此一次可见）。
func (s *store) createAgent(name, hostname, osName, arch, ip, meta string) (*Agent, string, error) {
	now := time.Now().Unix()
	a := &Agent{
		ID:        "am_" + randHex(8),
		Name:      name,
		Hostname:  hostname,
		OS:        osName,
		Arch:      arch,
		IP:        ip,
		Meta:      meta,
		CreatedAt: now,
		LastSeen:  now,
	}
	raw := "amh_" + randToken(24)
	_, err := s.db.Exec(
		`INSERT INTO agents (id, name, hostname, os, arch, ip, meta, token_hash, created_at, last_seen)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Name, a.Hostname, a.OS, a.Arch, a.IP, a.Meta, hashToken(raw), a.CreatedAt, a.LastSeen,
	)
	if err != nil {
		return nil, "", err
	}
	return a, raw, nil
}

// agentByToken 按心跳令牌（哈希比对）查找 Agent。
func (s *store) agentByToken(token string) (*Agent, error) {
	row := s.db.QueryRow(
		`SELECT id, name, hostname, os, arch, ip, meta, created_at, last_seen
		 FROM agents WHERE token_hash = ?`, hashToken(token))
	var a Agent
	if err := row.Scan(&a.ID, &a.Name, &a.Hostname, &a.OS, &a.Arch, &a.IP, &a.Meta, &a.CreatedAt, &a.LastSeen); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *store) touchHeartbeat(id, ip, meta string) error {
	if meta == "" {
		_, err := s.db.Exec(`UPDATE agents SET last_seen = ?, ip = ? WHERE id = ?`, time.Now().Unix(), ip, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE agents SET last_seen = ?, ip = ?, meta = ? WHERE id = ?`, time.Now().Unix(), ip, meta, id)
	return err
}

func (s *store) listAgents() ([]Agent, error) {
	rows, err := s.db.Query(
		`SELECT id, name, hostname, os, arch, ip, meta, created_at, last_seen FROM agents ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.Name, &a.Hostname, &a.OS, &a.Arch, &a.IP, &a.Meta, &a.CreatedAt, &a.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *store) deleteAgent(id string) error {
	_, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	return err
}

// validMeta 要求 meta 为空或合法 JSON 对象文本。
func validMeta(s string) bool {
	if s == "" {
		return true
	}
	var v map[string]any
	return json.Unmarshal([]byte(s), &v) == nil
}
