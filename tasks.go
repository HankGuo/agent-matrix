package main

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// 任务内容边界：纯文本，附件留待下一阶段。
const (
	titleMax      = 120
	contentMax    = 16 << 10
	resultMax     = 32 << 10
	maxAssignees  = 20
	pullLimit     = 20 // 单次拉取最多返回的指派数
	taskListLimit = 200
	staleAfter    = 10 * time.Minute // delivered 超过该时长无结果视为疑似卡住
)

// aggregateStatus 由指派集合实时聚合任务整体状态，不落库。
func aggregateStatus(t *Task, as []Assignment) string {
	if t.CanceledAt != nil {
		return "canceled"
	}
	done, failed, delivered, canceled := 0, 0, 0, 0
	for _, a := range as {
		switch a.Status {
		case AsDone:
			done++
		case AsFailed:
			failed++
		case AsDelivered:
			delivered++
		case AsCanceled:
			canceled++
		}
	}
	total := len(as)
	switch {
	case total > 0 && done == total:
		return "done"
	case done+failed == total && total > 0:
		if failed == total {
			return "failed"
		}
		return "partial"
	case delivered > 0:
		return "running"
	case total > 0 && canceled == total:
		return "canceled" // 全部指派随 Agent 删除等原因被取消
	default:
		return "pending"
	}
}

// assignmentView 是管理端看到的指派视图，附带 Agent 在线与卡住标记。
type assignmentView struct {
	Assignment
	Online bool `json:"online"`
	Stale  bool `json:"stale"` // delivered 超过 staleAfter 无结果
}

func (s *server) assignmentViews(as []Assignment, withResult bool) []assignmentView {
	now := time.Now().Unix()
	timeout := int64(s.cfg.OnlineTimeout.Seconds())
	out := make([]assignmentView, 0, len(as))
	for _, a := range as {
		if a.AgentName == "" {
			a.AgentName = "（已删除）"
		}
		v := assignmentView{
			Assignment: a,
			Online:     a.AgentLastSeen > 0 && now-a.AgentLastSeen <= timeout,
			Stale:      a.Status == AsDelivered && a.DeliveredAt != nil && now-*a.DeliveredAt > int64(staleAfter.Seconds()),
		}
		if !withResult {
			v.Result = "" // 列表不携带结果全文
			if a.ResultAt != nil {
				v.Result = "…" // 仅标记有结果，前端显示是否有内容可看
			}
		}
		out = append(out, v)
	}
	return out
}

// agentFromRequest 按 Bearer amh_ 心跳令牌识别 Agent，失败时写入 401 并返回 nil。
func (s *server) agentFromRequest(w http.ResponseWriter, r *http.Request) *Agent {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(token, "amh_") {
		writeError(w, http.StatusUnauthorized, "缺少心跳令牌")
		return nil
	}
	a, err := s.store.agentByToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "心跳令牌无效")
		return nil
	}
	return a
}

// ---- Agent 侧接口 ----

// handlePullTasks 拉取指派给自己的待执行任务，原子置为 delivered。
func (s *server) handlePullTasks(w http.ResponseWriter, r *http.Request) {
	a := s.agentFromRequest(w, r)
	if a == nil {
		return
	}
	if !s.pullRl.allow("pull:" + a.ID) {
		writeError(w, http.StatusTooManyRequests, "拉取过于频繁，请稍后再试")
		return
	}
	tasks, err := s.store.pullAssignments(a.ID, pullLimit)
	if err != nil {
		log.Printf("拉取任务失败 (%s): %v", a.ID, err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	// 附带输入件清单（拉取现场组装相对下载 URL，Agent 用 Bearer 令牌下载）
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.TaskID)
	}
	attMap, err := s.store.inputAttachmentsForTasks(ids)
	if err != nil {
		log.Printf("查询任务附件失败 (%s): %v", a.ID, err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	type pullView struct {
		pulledTask
		Attachments []attachmentView `json:"attachments,omitempty"`
	}
	out := make([]pullView, 0, len(tasks))
	for _, t := range tasks {
		v := pullView{pulledTask: t}
		for i := range attMap[t.TaskID] {
			v.Attachments = append(v.Attachments, attViewOf(&attMap[t.TaskID][i]))
		}
		out = append(out, v)
	}
	if len(tasks) > 0 {
		log.Printf("Agent %s 拉取了 %d 个任务", a.ID, len(tasks))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out, "server_time": time.Now().Unix()})
}

// handleWriteResult 回写执行结果：仅允许对自己的 delivered 指派写一次。
func (s *server) handleWriteResult(w http.ResponseWriter, r *http.Request) {
	a := s.agentFromRequest(w, r)
	if a == nil {
		return
	}
	id := r.PathValue("id")
	if !strings.HasPrefix(id, "tsa_") {
		writeError(w, http.StatusBadRequest, "无效的指派 ID")
		return
	}
	var req struct {
		Status string `json:"status"`
		Result string `json:"result"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status != AsDone && req.Status != AsFailed {
		writeError(w, http.StatusBadRequest, "status 只能是 done 或 failed")
		return
	}
	if len(req.Result) > resultMax {
		writeError(w, http.StatusBadRequest, "result 不能超过 32KB")
		return
	}
	err := s.store.writeResult(id, a.ID, req.Status, req.Result)
	switch {
	case err == nil:
		log.Printf("任务结果已回写: 指派 %s 状态 %s (%s)", id, req.Status, a.ID)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, errAssignNotFound):
		writeError(w, http.StatusNotFound, "指派不存在")
	case errors.Is(err, errAssignState):
		writeError(w, http.StatusConflict, "该指派不在可回写状态（未拉取、已回写或已取消）")
	default:
		log.Printf("回写任务结果失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
	}
}

// ---- 管理端接口 ----

// handleCreateTask 创建任务并 @ 一个或多个 Agent。
// 支持两种请求体：JSON（纯文本任务）；multipart/form-data（带附件，
// 字段 title / content / agent_ids / desc×N / file×N，desc 与 file 按顺序配对）。
func (s *server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var (
		title, content string
		agentIDs       []string
		files          []incomingFile
	)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		var ok bool
		title, content, agentIDs, files, ok = s.parseTaskMultipart(w, r)
		if !ok {
			return
		}
	} else {
		var req struct {
			Title    string   `json:"title"`
			Content  string   `json:"content"`
			AgentIDs []string `json:"agent_ids"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		title, content, agentIDs = req.Title, req.Content, req.AgentIDs
	}
	// 出错清理：附件已落盘但任务未建成时删除临时文件
	cleanupFiles := func() {
		for _, f := range files {
			_ = s.blob.Delete(f.key)
		}
	}
	title = clean(title, titleMax)
	content = strings.TrimSpace(content)
	if title == "" || content == "" {
		cleanupFiles()
		writeError(w, http.StatusBadRequest, "标题和内容不能为空")
		return
	}
	if len(content) > contentMax {
		cleanupFiles()
		writeError(w, http.StatusBadRequest, "内容不能超过 16KB")
		return
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		for _, one := range strings.Split(id, ",") { // multipart 表单允许逗号合并
			one = strings.TrimSpace(one)
			if !seen[one] && strings.HasPrefix(one, "am_") {
				seen[one] = true
				ids = append(ids, one)
			}
		}
	}
	if len(ids) == 0 || len(ids) > maxAssignees {
		cleanupFiles()
		writeError(w, http.StatusBadRequest, "请选择 1-20 个 Agent")
		return
	}
	ok, err := s.store.agentsExist(ids)
	if err != nil {
		cleanupFiles()
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if !ok {
		cleanupFiles()
		writeError(w, http.StatusBadRequest, "包含不存在的 Agent")
		return
	}
	t, err := s.store.createTask(title, content, ids)
	if err != nil {
		cleanupFiles()
		log.Printf("创建任务失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if err := s.saveInputAttachments(t.ID, files); err != nil {
		_, _ = s.store.deleteTask(t.ID)
		cleanupFiles()
		log.Printf("登记附件失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	log.Printf("任务已创建: %s %q，指派给 %d 个 Agent，附件 %d 个", t.ID, t.Title, len(ids), len(files))
	writeJSON(w, http.StatusCreated, map[string]any{"task": t})
}

// handleListTasks 任务列表（不含结果全文）。
func (s *server) handleListTasks(w http.ResponseWriter, _ *http.Request) {
	tasks, assigns, err := s.store.listTasks(taskListLimit)
	if err != nil {
		log.Printf("查询任务列表失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	type taskView struct {
		Task
		Status      string           `json:"status"`
		Assignments []assignmentView `json:"assignments"`
	}
	out := make([]taskView, 0, len(tasks))
	for i := range tasks {
		as := assigns[tasks[i].ID]
		out = append(out, taskView{
			Task:        tasks[i],
			Status:      aggregateStatus(&tasks[i], as),
			Assignments: s.assignmentViews(as, false),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

// handleTaskDetail 任务详情（含各指派结果全文）。
func (s *server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.store.taskByID(id)
	if errors.Is(err, errTaskNotFound) {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	_, assigns, err := s.store.listTasks(taskListLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	as := assigns[id]
	// 附件：输入件平铺列出，产出件按指派分组
	atts, err := s.store.taskAttachments(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	inputs := []Attachment{}
	outputs := map[string][]Attachment{}
	for _, a := range atts {
		if a.Kind == AttIn {
			inputs = append(inputs, a)
		} else {
			outputs[a.AssignmentID] = append(outputs[a.AssignmentID], a)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task":        t,
		"status":      aggregateStatus(t, as),
		"assignments": s.assignmentViews(as, true),
		"inputs":      inputs,
		"outputs":     outputs,
	})
}

// handleCancelTask 取消任务，未结束的指派全部置为 canceled。
func (s *server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	err := s.store.cancelTask(r.PathValue("id"))
	switch {
	case err == nil:
		log.Printf("任务已取消: %s", r.PathValue("id"))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, errTaskNotFound):
		writeError(w, http.StatusNotFound, "任务不存在")
	case errors.Is(err, errTaskFinished):
		writeError(w, http.StatusConflict, errTaskFinished.Error())
	default:
		log.Printf("取消任务失败: %v", err)
		writeError(w, http.StatusInternalServerError, "内部错误")
	}
}

// handleRequeueAssignment 把疑似卡住的指派重置回 pending，允许 Agent 重新拉取。
func (s *server) handleRequeueAssignment(w http.ResponseWriter, r *http.Request) {
	err := s.store.requeueAssignment(r.PathValue("id"))
	switch {
	case err == nil:
		log.Printf("指派已重新投递: %s", r.PathValue("id"))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, errAssignState):
		writeError(w, http.StatusConflict, "仅「执行中」的指派可以重新投递")
	default:
		writeError(w, http.StatusInternalServerError, "内部错误")
	}
}

// handleTaskLoopPrompt 给已接入的老 Agent 生成「补充任务能力」指令（不含任何密钥）。
func (s *server) handleTaskLoopPrompt(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"prompt": buildTaskLoopPrompt(s.baseURL())})
}
