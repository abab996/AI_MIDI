package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"aimidi/internal/chat"
)

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
	callback := func(event map[string]any) error {
		_, err := fmt.Fprint(w, sseEvent(event))
		if flusher != nil {
			flusher.Flush()
		}
		return err
	}

	// 传入 req.Context() 仅用于携带请求元数据；ChatStream 内部用
	// WithoutCancel 切断取消传播——前端断开 SSE 时任务转后台续跑
	// （不中止生成），用户主动停止经 tasks.TaskCancelChan 传导
	_ = chat.ChatStream(req.Context(), in.ProjectID, in.Message, in.Edit, false, in.TaskID, callback)
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
	callback := func(event map[string]any) error {
		_, err := fmt.Fprint(w, sseEvent(event))
		if flusher != nil {
			flusher.Flush()
		}
		return err
	}

	_ = chat.AnswerStream(req.Context(), in.ProjectID, in.QuestionID, in.Answers, callback)
}
