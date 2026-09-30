package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"melodymate/internal/chat"
)

// sseCallback 构造带写超时的事件回调。必须给每次 Fprint 设写截止时间：
// 客户端停止读取（浏览器后台标签节流、僵死连接）时 TCP 发送缓冲填满，
// Fprint 会无限期阻塞——而该回调可能在持有会话锁的状态下被调用，
// 一个停滞的连接就足以冻结整个项目的全部请求
func sseCallback(w http.ResponseWriter, flusher http.Flusher) func(map[string]any) error {
	rc := http.NewResponseController(w)
	return func(event map[string]any) error {
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		_, err := fmt.Fprint(w, sseEvent(event))
		if flusher != nil {
			flusher.Flush()
		}
		return err
	}
}

func (r *Router) handleChat(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var in struct {
		ProjectID string  `json:"project_id"`
		Message   string  `json:"message"`
		Edit      bool    `json:"edit"`
		TaskID    *string `json:"task_id"`
	}

	if err := json.NewDecoder(req.Body).Decode(&in); err != nil || in.ProjectID == "" {
		writeError(w, http.StatusBadRequest, "缺少 project_id 或无效请求")
		return
	}

	flusher := setupSSEHeaders(w)
	callback := sseCallback(w, flusher)

	// 传入 req.Context() 仅用于携带请求元数据；ChatStream 内部用
	// WithoutCancel 切断取消传播——前端断开 SSE 时任务转后台续跑
	// （不中止生成），用户主动停止经 tasks.TaskCancelChan 传导
	if err := chat.ChatStream(req.Context(), in.ProjectID, in.Message, in.Edit, false, in.TaskID, callback); err != nil {
		// ChatStream 在发出任何事件前就失败（配置缺失等）时，SSE 头已发出，
		// 必须补发 error/done 事件，否则前端停在「生成中」无限转圈
		_ = callback(map[string]any{"type": "error", "message": err.Error()})
		_ = callback(map[string]any{"type": "done"})
	}
}

func (r *Router) handleAnswer(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var in struct {
		ProjectID  string `json:"project_id"`
		QuestionID string `json:"question_id"`
		Answers    any    `json:"answers"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil || in.ProjectID == "" || in.QuestionID == "" {
		writeError(w, http.StatusBadRequest, "缺少参数或无效请求")
		return
	}

	flusher := setupSSEHeaders(w)
	callback := sseCallback(w, flusher)

	if err := chat.AnswerStream(req.Context(), in.ProjectID, in.QuestionID, in.Answers, callback); err != nil {
		_ = callback(map[string]any{"type": "error", "message": err.Error()})
		_ = callback(map[string]any{"type": "done"})
	}
}

// handleApproval POST /api/approval：MM 编译/安装的批准卡回执。
// 与提问卡同规：这是"用户点过按钮"的唯一凭据来源，模型无法自行产生。
// 本身不产生对话流（批准后由模型带 approval_id 重试工具调用）。
func (r *Router) handleApproval(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var in struct {
		ProjectID  string `json:"project_id"`
		ApprovalID string `json:"approval_id"`
		Allow      bool   `json:"allow"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil || in.ProjectID == "" || in.ApprovalID == "" {
		writeError(w, http.StatusBadRequest, "缺少参数或无效请求")
		return
	}

	if !in.Allow {
		chat.RejectBuildApproval(in.ProjectID, in.ApprovalID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": false})
		return
	}
	if !chat.ApproveBuildApproval(in.ProjectID, in.ApprovalID) {
		writeError(w, http.StatusNotFound, "确认请求不存在或已过期，请让 AI 重新发起")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": true})
}
