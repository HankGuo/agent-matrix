package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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
CREATE TABLE IF NOT EXISTS tasks (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    content     TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    canceled_at INTEGER
);
-- 指派：一个任务 @ 多个 Agent，每个 Agent 一条，状态独立流转。
-- 同一任务可多次追加指令（继续任务），每次追加是一轮（seq 递增）、每个目标 Agent 一条新指派；
-- content 是本轮指令快照（首轮 = 任务正文）。OpenClaw 按任务 ID 绑定会话，追加轮自动带上上文。
-- agent_id 不建外键：Agent 删除后指派保留为历史（状态置 canceled）。
CREATE TABLE IF NOT EXISTS task_assignments (
    id           TEXT PRIMARY KEY,
    task_id      TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id     TEXT NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 1,
    content      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending',
    created_at   INTEGER NOT NULL DEFAULT 0,
    delivered_at INTEGER,
    result       TEXT NOT NULL DEFAULT '',
    result_at    INTEGER
);
CREATE INDEX IF NOT EXISTS idx_assign_agent ON task_assignments(agent_id, status);
-- 附件：输入件挂在任务上（assignment_id 为空），产出件挂在指派上。
-- 字节不在数据库里，key 指向磁盘文件（见 blob.go）。
CREATE TABLE IF NOT EXISTS task_attachments (
    id            TEXT PRIMARY KEY,
    task_id       TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    assignment_id TEXT NOT NULL DEFAULT '',
    agent_id      TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL,
    key           TEXT UNIQUE NOT NULL,
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    size          INTEGER NOT NULL,
    mime          TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_att_task ON task_attachments(task_id, kind);
-- 下线墓碑：Agent 删除后其心跳令牌哈希在此保留一段时间，
-- 心跳命中墓碑返回 410，Agent 端据此自卸载；定期惰性清理。
CREATE TABLE IF NOT EXISTS decommissioned_tokens (
    token_hash TEXT PRIMARY KEY,
    deleted_at INTEGER NOT NULL
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

// getSetting 读取设置项。
func (s *store) getSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, err
}

// setSetting 写入设置项（存在则覆盖）。
func (s *store) setSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// defaultPollInterval 是未配置时的默认轮询间隔（秒）。
const defaultPollInterval = 60

// pollInterval 返回全局轮询间隔（秒），随心跳响应下发，各实例据此机械调整
// 本机定时任务。缺失或非法值回退默认。
func (s *store) pollInterval() int {
	v, err := s.getSetting("poll_interval")
	if err != nil {
		return defaultPollInterval
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 10 || n > 3600 {
		return defaultPollInterval
	}
	return n
}

// sessionSecret 返回持久的会话签名密钥，不存在则生成。
func (s *store) sessionSecret() (string, error) {
	v, err := s.getSetting("session_secret")
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	v = randToken(32)
	return v, s.setSetting("session_secret", v)
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

var errNameTaken = errors.New("登记名称已被占用")

// agentNameExists 报告是否已存在同名 Agent。名称全局唯一，注册前置校验，
// 避免同名冲突时白白核销一次性令牌。
func (s *store) agentNameExists(name string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE name = ?`, name).Scan(&n)
	return n > 0, err
}

// createAgent 创建 Agent 记录，返回记录与明文心跳令牌（仅此一次可见）。
// 名称全局唯一：同事务内先查后插，重名返回 errNameTaken。
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
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM agents WHERE name = ?`, name).Scan(&n); err != nil {
		return nil, "", err
	}
	if n > 0 {
		return nil, "", errNameTaken
	}
	if _, err := tx.Exec(
		`INSERT INTO agents (id, name, hostname, os, arch, ip, meta, token_hash, created_at, last_seen)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Name, a.Hostname, a.OS, a.Arch, a.IP, a.Meta, hashToken(raw), a.CreatedAt, a.LastSeen,
	); err != nil {
		return nil, "", err
	}
	return a, raw, tx.Commit()
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

// decommissionAgent 下线 Agent：令牌哈希转入墓碑表（心跳将收到 410 触发自卸载），
// 再删除 Agent 记录；同时惰性清理 30 天前的墓碑。
func (s *store) decommissionAgent(id string) error {
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var tokenHash string
	err = tx.QueryRow(`SELECT token_hash FROM agents WHERE id = ?`, id).Scan(&tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return errAgentNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO decommissioned_tokens (token_hash, deleted_at) VALUES (?,?)`,
		tokenHash, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM decommissioned_tokens WHERE deleted_at < ?`, now-30*86400); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM agents WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// isDecommissioned 报告该心跳令牌是否属于已下线的 Agent。
func (s *store) isDecommissioned(token string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM decommissioned_tokens WHERE token_hash = ?`, hashToken(token)).Scan(&n)
	return n > 0, err
}

var errAgentNotFound = errors.New("Agent 不存在")

// validMeta 要求 meta 为空或合法 JSON 对象文本。
func validMeta(s string) bool {
	if s == "" {
		return true
	}
	var v map[string]any
	return json.Unmarshal([]byte(s), &v) == nil
}

// ---- 任务与指派 ----

// 指派状态机：pending → delivered → done/failed；pending/delivered 可被取消为 canceled。
const (
	AsPending   = "pending"
	AsDelivered = "delivered"
	AsDone      = "done"
	AsFailed    = "failed"
	AsCanceled  = "canceled"
)

// Task 是一条派发给一个或多个 Agent 的纯文本任务。
type Task struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	CreatedAt  int64  `json:"created_at"`
	CanceledAt *int64 `json:"canceled_at,omitempty"`
}

// Assignment 是任务指派给单个 Agent 的执行单元。同一任务每轮追加产生一组新指派（Seq 递增）。
type Assignment struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`
	AgentID       string `json:"agent_id"`
	AgentName     string `json:"agent_name"` // LEFT JOIN agents，空串表示 Agent 已删除
	AgentLastSeen int64  `json:"-"`
	Seq           int    `json:"seq"`
	Content       string `json:"content"` // 本轮指令快照
	CreatedAt     int64  `json:"created_at"`
	Status        string `json:"status"`
	DeliveredAt   *int64 `json:"delivered_at,omitempty"`
	Result        string `json:"result,omitempty"`
	ResultAt      *int64 `json:"result_at,omitempty"`
}

// agentsExist 校验给定 Agent ID 是否全部存在。
func (s *store) agentsExist(ids []string) (bool, error) {
	for _, id := range ids {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE id = ?`, id).Scan(&n); err != nil {
			return false, err
		}
		if n == 0 {
			return false, nil
		}
	}
	return true, nil
}

// createTask 在一个事务里创建任务及其全部首轮指派（seq=1，指令快照=任务正文）。
func (s *store) createTask(title, content string, agentIDs []string) (*Task, error) {
	t := &Task{ID: "tsk_" + randHex(8), Title: title, Content: content, CreatedAt: time.Now().Unix()}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO tasks (id, title, content, created_at) VALUES (?,?,?,?)`,
		t.ID, t.Title, t.Content, t.CreatedAt); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	for _, aid := range agentIDs {
		if _, err := tx.Exec(
			`INSERT INTO task_assignments (id, task_id, agent_id, seq, content, status, created_at) VALUES (?,?,?,1,?,'pending',?)`,
			"tsa_"+randHex(8), t.ID, aid, content, now); err != nil {
			return nil, err
		}
	}
	return t, tx.Commit()
}

var errTaskCanceled = errors.New("任务已取消，不能继续追加")

// createFollowup 给已有任务追加一轮指令：每个目标 Agent 生成 seq+1 的新指派。
// agentIDs 为空时默认沿用该任务当前仍存在的全部 Agent。
func (s *store) createFollowup(taskID, content string, agentIDs []string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var canceledAt *int64
	err = tx.QueryRow(`SELECT canceled_at FROM tasks WHERE id = ?`, taskID).Scan(&canceledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errTaskNotFound
	}
	if err != nil {
		return 0, err
	}
	if canceledAt != nil {
		return 0, errTaskCanceled
	}
	var seq int
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM task_assignments WHERE task_id = ?`, taskID).Scan(&seq); err != nil {
		return 0, err
	}
	if len(agentIDs) == 0 {
		rows, err := tx.Query(
			`SELECT DISTINCT a.agent_id FROM task_assignments a JOIN agents g ON g.id = a.agent_id
			 WHERE a.task_id = ? ORDER BY a.agent_id`, taskID)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			agentIDs = append(agentIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
	}
	if len(agentIDs) == 0 {
		return 0, errAgentNotFound
	}
	now := time.Now().Unix()
	for _, aid := range agentIDs {
		if _, err := tx.Exec(
			`INSERT INTO task_assignments (id, task_id, agent_id, seq, content, status, created_at) VALUES (?,?,?,?,?,'pending',?)`,
			"tsa_"+randHex(8), taskID, aid, seq, content, now); err != nil {
			return 0, err
		}
	}
	return seq, tx.Commit()
}

// listTasks 返回最近的任务及其指派（按任务创建时间倒序，指派按创建顺序）。
func (s *store) listTasks(limit int) ([]Task, map[string][]Assignment, error) {
	rows, err := s.db.Query(
		`SELECT id, title, content, created_at, canceled_at FROM tasks ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	keep := map[string]bool{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Content, &t.CreatedAt, &t.CanceledAt); err != nil {
			return nil, nil, err
		}
		keep[t.ID] = true
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	assigns := map[string][]Assignment{}
	arows, err := s.db.Query(
		`SELECT a.id, a.task_id, a.agent_id, COALESCE(g.name, ''), a.seq, a.content, a.created_at, a.status,
		        a.delivered_at, a.result, a.result_at, COALESCE(g.last_seen, 0)
		 FROM task_assignments a LEFT JOIN agents g ON g.id = a.agent_id
		 ORDER BY a.seq, a.rowid`)
	if err != nil {
		return nil, nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var a Assignment
		if err := arows.Scan(&a.ID, &a.TaskID, &a.AgentID, &a.AgentName, &a.Seq, &a.Content, &a.CreatedAt, &a.Status,
			&a.DeliveredAt, &a.Result, &a.ResultAt, &a.AgentLastSeen); err != nil {
			return nil, nil, err
		}
		if keep[a.TaskID] {
			assigns[a.TaskID] = append(assigns[a.TaskID], a)
		}
	}
	return tasks, assigns, arows.Err()
}

// taskByID 读取单个任务，不存在时返回 errTaskNotFound。
func (s *store) taskByID(id string) (*Task, error) {
	var t Task
	err := s.db.QueryRow(
		`SELECT id, title, content, created_at, canceled_at FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.Title, &t.Content, &t.CreatedAt, &t.CanceledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

var errTaskNotFound = errors.New("任务不存在")
var errTaskFinished = errors.New("任务已全部结束，无需取消")
var errAssignState = errors.New("指派当前状态不允许该操作")
var errAssignNotFound = errors.New("指派不存在")

// cancelTask 取消任务：标记取消时间，并把未结束的指派置为 canceled。
func (s *store) cancelTask(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var canceledAt *int64
	err = tx.QueryRow(`SELECT canceled_at FROM tasks WHERE id = ?`, id).Scan(&canceledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errTaskNotFound
	}
	if err != nil {
		return err
	}
	if canceledAt != nil {
		return errTaskFinished
	}
	var open int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM task_assignments WHERE task_id = ? AND status IN ('pending','delivered')`, id).
		Scan(&open); err != nil {
		return err
	}
	if open == 0 {
		return errTaskFinished
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE tasks SET canceled_at = ? WHERE id = ?`, now, id); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE task_assignments SET status = 'canceled' WHERE task_id = ? AND status IN ('pending','delivered')`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// requeueAssignment 把疑似卡住的 delivered 指派重置回 pending，允许 Agent 重新拉取。
func (s *store) requeueAssignment(id string) error {
	res, err := s.db.Exec(
		`UPDATE task_assignments SET status = 'pending', delivered_at = NULL WHERE id = ? AND status = 'delivered'`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errAssignState
	}
	return nil
}

// pulledTask 是 Agent 拉取接口返回的任务视图。
type pulledTask struct {
	AssignmentID string `json:"assignment_id"`
	TaskID       string `json:"task_id"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	CreatedAt    int64  `json:"created_at"`
}

// pullAssignments 原子地把 Agent 的待拉取指派置为 delivered 并返回，重复拉取不会重复投递。
func (s *store) pullAssignments(agentID string, limit int) ([]pulledTask, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(
		`SELECT a.id, a.task_id, t.title, a.content, t.created_at
		 FROM task_assignments a JOIN tasks t ON t.id = a.task_id
		 WHERE a.agent_id = ? AND a.status = 'pending' AND t.canceled_at IS NULL
		 ORDER BY t.created_at ASC, a.seq ASC, a.rowid ASC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, err
	}
	out := []pulledTask{}
	for rows.Next() {
		var p pulledTask
		if err := rows.Scan(&p.AssignmentID, &p.TaskID, &p.Title, &p.Content, &p.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	for _, p := range out {
		if _, err := tx.Exec(
			`UPDATE task_assignments SET status = 'delivered', delivered_at = ? WHERE id = ? AND status = 'pending'`,
			now, p.AssignmentID); err != nil {
			return nil, err
		}
	}
	return out, tx.Commit()
}

// writeResult 回写执行结果：只允许对自己的 delivered 指派写一次。
func (s *store) writeResult(assignmentID, agentID, status, result string) error {
	res, err := s.db.Exec(
		`UPDATE task_assignments SET status = ?, result = ?, result_at = ?
		 WHERE id = ? AND agent_id = ? AND status = 'delivered'`,
		status, result, time.Now().Unix(), assignmentID, agentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var st, aid string
	err = s.db.QueryRow(`SELECT status, agent_id FROM task_assignments WHERE id = ?`, assignmentID).Scan(&st, &aid)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && aid != agentID) {
		return errAssignNotFound // 不暴露他人指派的存在性
	}
	if err != nil {
		return err
	}
	return errAssignState
}

// cancelOpenAssignmentsForAgent 删除 Agent 时取消其未结束的指派，历史保留。
func (s *store) cancelOpenAssignmentsForAgent(agentID string) error {
	_, err := s.db.Exec(
		`UPDATE task_assignments SET status = 'canceled' WHERE agent_id = ? AND status IN ('pending','delivered')`, agentID)
	return err
}

// ---- 附件 ----

// 附件方向：输入件随任务下发，产出件由 Agent 随结果上传。
const (
	AttIn  = "in"
	AttOut = "out"
)

// Attachment 是一条附件元数据；字节在磁盘上，Key 是存储键。
type Attachment struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	AssignmentID string `json:"assignment_id,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	Kind         string `json:"kind"`
	Key          string `json:"-"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Size         int64  `json:"size"`
	Mime         string `json:"mime"`
	CreatedAt    int64  `json:"created_at"`
}

// createAttachment 写入附件元数据。
func (s *store) createAttachment(a *Attachment) error {
	_, err := s.db.Exec(
		`INSERT INTO task_attachments (id, task_id, assignment_id, agent_id, kind, key, name, description, size, mime, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.TaskID, a.AssignmentID, a.AgentID, a.Kind, a.Key, a.Name, a.Description, a.Size, a.Mime, a.CreatedAt)
	return err
}

const attCols = `id, task_id, assignment_id, agent_id, kind, key, name, description, size, mime, created_at`

func scanAtt(row interface{ Scan(...any) error }) (*Attachment, error) {
	var a Attachment
	err := row.Scan(&a.ID, &a.TaskID, &a.AssignmentID, &a.AgentID, &a.Kind, &a.Key,
		&a.Name, &a.Description, &a.Size, &a.Mime, &a.CreatedAt)
	return &a, err
}

// attachmentByID 读取单条附件元数据。
func (s *store) attachmentByID(id string) (*Attachment, error) {
	a, err := scanAtt(s.db.QueryRow(`SELECT `+attCols+` FROM task_attachments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errAttNotFound
	}
	return a, err
}

// taskAttachments 返回一个任务的全部附件（先输入件后产出件，各自按创建顺序）。
func (s *store) taskAttachments(taskID string) ([]Attachment, error) {
	rows, err := s.db.Query(
		`SELECT `+attCols+` FROM task_attachments WHERE task_id = ? ORDER BY kind, rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		a, err := scanAtt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// inputAttachmentsForTasks 批量取一组任务的输入件，供拉取接口拼装。
func (s *store) inputAttachmentsForTasks(taskIDs []string) (map[string][]Attachment, error) {
	out := map[string][]Attachment{}
	for _, id := range taskIDs {
		rows, err := s.db.Query(
			`SELECT `+attCols+` FROM task_attachments WHERE task_id = ? AND kind = 'in' ORDER BY rowid`, id)
		if err != nil {
			return nil, err
		}
		list := []Attachment{}
		for rows.Next() {
			a, err := scanAtt(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			list = append(list, *a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(list) > 0 {
			out[id] = list
		}
	}
	return out, nil
}

var errAttNotFound = errors.New("附件不存在")

// agentCanReadInput 校验该输入件是否属于这个 Agent 被指派过的任务。
func (s *store) agentCanReadInput(taskID, agentID string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM task_assignments WHERE task_id = ? AND agent_id = ?`, taskID, agentID).Scan(&n)
	return n > 0, err
}

// assignmentUploadTarget 校验产出上传：指派存在、属于该 Agent、且处于 delivered。
// 通过则返回任务 ID。
func (s *store) assignmentUploadTarget(assignmentID, agentID string) (string, error) {
	var st, aid, tid string
	err := s.db.QueryRow(
		`SELECT status, agent_id, task_id FROM task_assignments WHERE id = ?`, assignmentID).
		Scan(&st, &aid, &tid)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && aid != agentID) {
		return "", errAssignNotFound
	}
	if err != nil {
		return "", err
	}
	if st != AsDelivered {
		return "", errAssignState
	}
	return tid, nil
}

// deleteTask 删除任务：指派与附件记录随外键级联删除，返回待清理的存储键。
func (s *store) deleteTask(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT key FROM task_attachments WHERE task_id = ?`, id)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errTaskNotFound
	}
	return keys, nil
}

// deleteAgentOutputs 删除 Agent 的全部产出附件记录，返回待清理的存储键。
func (s *store) deleteAgentOutputs(agentID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT key FROM task_attachments WHERE agent_id = ? AND kind = 'out'`, agentID)
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`DELETE FROM task_attachments WHERE agent_id = ? AND kind = 'out'`, agentID)
	return keys, err
}
