package main

import (
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"
)

// attachmentView 是拉取接口里给 Agent 看的附件条目：相对 URL，用 Bearer 令牌下载。
type attachmentView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Size        int64  `json:"size"`
	Mime        string `json:"mime"`
	URL         string `json:"url"`
}

func attViewOf(a *Attachment) attachmentView {
	return attachmentView{
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		Size:        a.Size,
		Mime:        a.Mime,
		URL:         "/api/agent/attachments/" + a.ID,
	}
}

// incomingFile 是 multipart 表单里流式收下、待落库绑定的一个文件。
type incomingFile struct {
	key  string
	name string
	desc string
	size int64
	mime string
}

// parseTaskMultipart 流式解析建任务的 multipart 表单：文件边收边落盘（先于任务记录存在），
// 解析失败时清理已落盘的临时文件。desc 与 file 按到达顺序一一配对——每个 desc 必须在
// 其对应 file 之前到达（前端按 desc_i + file_i 的顺序交替追加）。
func (s *server) parseTaskMultipart(w http.ResponseWriter, r *http.Request) (title, content string, agentIDs []string, files []incomingFile, ok bool) {
	// 兜底上限：10 个文件 × 100MB + 字段余量；单文件上限在 blob.Put 里硬截断。
	r.Body = http.MaxBytesReader(w, r.Body, (maxAttachmentSize+1<<20)*maxAttachmentsPerTask)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 multipart")
		return
	}
	fail := func(code int, msg string) {
		for _, f := range files {
			_ = s.blob.Delete(f.key)
		}
		writeError(w, code, msg)
	}
	descs := []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(http.StatusBadRequest, "multipart 解析失败")
			return
		}
		name := part.FormName()
		if part.FileName() != "" {
			if len(files) >= maxAttachmentsPerTask {
				part.Close()
				fail(http.StatusBadRequest, "附件最多 10 个")
				return
			}
			idx := len(files)
			f := incomingFile{
				key:  "att_" + randHex(8),
				name: clean(part.FileName(), 120),
				mime: part.Header.Get("Content-Type"),
			}
			if idx < len(descs) {
				f.desc = descs[idx]
			}
			f.size, f.mime, err = s.blob.Put(f.key, part)
			part.Close()
			if errors.Is(err, errAttTooLarge) {
				fail(http.StatusBadRequest, errAttTooLarge.Error())
				return
			}
			if err != nil {
				log.Printf("附件落盘失败: %v", err)
				fail(http.StatusInternalServerError, "附件保存失败")
				return
			}
			if f.name == "" {
				f.name = "unnamed"
			}
			files = append(files, f)
			continue
		}
		val, _ := io.ReadAll(io.LimitReader(part, 64<<10))
		part.Close()
		switch name {
		case "title":
			title = string(val)
		case "content":
			content = string(val)
		case "agent_ids":
			agentIDs = append(agentIDs, string(val))
		case "desc":
			descs = append(descs, clean(string(val), 300))
		}
	}
	ok = true
	return
}

// saveInputAttachments 把已落盘的文件登记为任务的输入件。
func (s *server) saveInputAttachments(taskID string, files []incomingFile) error {
	for _, f := range files {
		if err := s.store.createAttachment(&Attachment{
			ID:          f.key,
			TaskID:      taskID,
			Kind:        AttIn,
			Key:         f.key,
			Name:        f.name,
			Description: f.desc,
			Size:        f.size,
			Mime:        f.mime,
			CreatedAt:   time.Now().Unix(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// inlinePreviewable 报告嗅探 MIME 是否属于可安全 inline 预览的白名单。
// 其余类型一律 attachment 强制下载，杜绝 HTML/SVG 存储型 XSS。
func inlinePreviewable(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml" ||
		strings.HasPrefix(mimeType, "audio/") ||
		strings.HasPrefix(mimeType, "video/") ||
		mimeType == "application/pdf"
}

// serveAttachment 流式下发附件：Content-Type 用嗅探值，文件名走 RFC 5987 编码，
// 默认强制下载，白名单类型 inline 并加 CSP sandbox。ServeContent 自带 Range 支持。
func (s *server) serveAttachment(w http.ResponseWriter, r *http.Request, att *Attachment) {
	f, _, err := s.blob.Open(att.Key)
	if errors.Is(err, errAttNotFound) {
		writeError(w, http.StatusNotFound, "附件文件已丢失")
		return
	}
	if err != nil {
		log.Printf("打开附件失败 (%s): %v", att.ID, err)
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	defer f.Close()

	disp := "attachment"
	if inlinePreviewable(att.Mime) && r.URL.Query().Get("download") == "" {
		disp = "inline"
		// 覆盖全局 CSP：inline 内容在沙箱里渲染，禁脚本。
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	// RFC 5987 文件名编码；遇到无法编码的字符时退化为不带文件名。
	if v := mime.FormatMediaType(disp, map[string]string{"filename": att.Name}); v != "" {
		w.Header().Set("Content-Disposition", v)
	} else {
		w.Header().Set("Content-Disposition", disp)
	}
	w.Header().Set("Content-Type", att.Mime)
	http.ServeContent(w, r, "", time.Time{}, f)
}

// handleAdminGetAttachment 管理员预览/下载附件（输入件与产出件均可）。
func (s *server) handleAdminGetAttachment(w http.ResponseWriter, r *http.Request) {
	att, err := s.store.attachmentByID(r.PathValue("id"))
	if errors.Is(err, errAttNotFound) {
		writeError(w, http.StatusNotFound, "附件不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	s.serveAttachment(w, r, att)
}

// handleAgentGetAttachment Agent 下载输入件：仅限自己被指派过的任务的输入件。
func (s *server) handleAgentGetAttachment(w http.ResponseWriter, r *http.Request) {
	a := s.agentFromRequest(w, r)
	if a == nil {
		return
	}
	att, err := s.store.attachmentByID(r.PathValue("id"))
	if errors.Is(err, errAttNotFound) {
		writeError(w, http.StatusNotFound, "附件不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if att.Kind != AttIn {
		writeError(w, http.StatusForbidden, "无权访问该附件")
		return
	}
	ok, err := s.store.agentCanReadInput(att.TaskID, a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "无权访问该附件")
		return
	}
	s.serveAttachment(w, r, att)
}

// handleUploadOutput Agent 上传产出文件：要求指派属于它且处于 delivered。
// 单文件 multipart（字段名 file，可选 desc），可多次调用上传多个。
func (s *server) handleUploadOutput(w http.ResponseWriter, r *http.Request) {
	a := s.agentFromRequest(w, r)
	if a == nil {
		return
	}
	aid := r.PathValue("id")
	if !strings.HasPrefix(aid, "tsa_") {
		writeError(w, http.StatusBadRequest, "无效的指派 ID")
		return
	}
	taskID, err := s.store.assignmentUploadTarget(aid, a.ID)
	switch {
	case errors.Is(err, errAssignNotFound):
		writeError(w, http.StatusNotFound, "指派不存在")
		return
	case errors.Is(err, errAssignState):
		writeError(w, http.StatusConflict, "该指派不在可上传状态（未拉取、已回写或已取消）")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "内部错误")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentSize+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 multipart")
		return
	}
	var desc string
	var saved *Attachment
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "multipart 解析失败")
			return
		}
		if part.FileName() == "" {
			if part.FormName() == "desc" {
				v, _ := io.ReadAll(io.LimitReader(part, 4<<10))
				desc = clean(string(v), 300)
			}
			part.Close()
			continue
		}
		att := &Attachment{
			ID:           "att_" + randHex(8),
			TaskID:       taskID,
			AssignmentID: aid,
			AgentID:      a.ID,
			Kind:         AttOut,
			Key:          "",
			Name:         clean(part.FileName(), 120),
			Description:  desc,
			CreatedAt:    time.Now().Unix(),
		}
		att.Key = att.ID
		att.Size, att.Mime, err = s.blob.Put(att.Key, part)
		part.Close()
		if errors.Is(err, errAttTooLarge) {
			writeError(w, http.StatusBadRequest, errAttTooLarge.Error())
			return
		}
		if err != nil {
			log.Printf("产出附件落盘失败: %v", err)
			writeError(w, http.StatusInternalServerError, "附件保存失败")
			return
		}
		if att.Name == "" {
			att.Name = "unnamed"
		}
		if err := s.store.createAttachment(att); err != nil {
			_ = s.blob.Delete(att.Key)
			log.Printf("产出附件登记失败: %v", err)
			writeError(w, http.StatusInternalServerError, "内部错误")
			return
		}
		saved = att
		// 只收第一个文件，剩余部分忽略
	}
	if saved == nil {
		writeError(w, http.StatusBadRequest, "未收到文件")
		return
	}
	log.Printf("产出附件已上传: %s（指派 %s，%d 字节）", saved.Name, aid, saved.Size)
	writeJSON(w, http.StatusCreated, map[string]any{"attachment": saved})
}

// deleteAttachmentsOfAgent 删除 Agent 时清理其全部产出附件（记录 + 文件）。
func (s *server) deleteAttachmentsOfAgent(agentID string) {
	keys, err := s.store.deleteAgentOutputs(agentID)
	if err != nil {
		log.Printf("清理 Agent 产出附件记录失败 (%s): %v", agentID, err)
		return
	}
	for _, k := range keys {
		if err := s.blob.Delete(k); err != nil {
			log.Printf("清理产出附件文件失败 (%s): %v", k, err)
		}
	}
}
