package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"aimidi/internal/chat"
	"aimidi/internal/tasks"
)

func (r *Router) handleTasks(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	pid := req.URL.Query().Get("project_id")
	tList := tasks.TaskList(pid)
	if tList == nil {
		tList = []tasks.TaskRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tList})
}

func (r *Router) handleTasksSub(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/api/tasks/")
	parts := strings.Split(path, "/")
	taskID := parts[0]

	if taskID == "" {
		writeError(w, http.StatusBadRequest, "缺少任务 ID")
		return
	}

	if len(parts) == 1 {
		if req.Method == http.MethodDelete {
			// 删除前记录归属并挑选切换目标：优先被删任务在创建序中的
			// 前一个任务（删的是第一个则取后一个），全部删完留空由会话层新建
			rec := tasks.TaskGet(taskID)
			var nextTaskID string
			if rec != nil {
				list := tasks.TaskList(rec.ProjectID)
				for i, t := range list {
					if t.ID == taskID {
						if i > 0 {
							nextTaskID = list[i-1].ID
						} else if len(list) > 1 {
							nextTaskID = list[i+1].ID
						}
						break
					}
				}
			}
			ok, detail := tasks.TaskDelete(taskID)
			if !ok {
				writeError(w, http.StatusBadRequest, detail)
				return
			}
			// 同步丢弃内存会话状态并切换当前任务——此前缺这一步，
			// 删除后工作台仍停留在已删任务的对话上
			if rec != nil {
				chat.DropTaskStateTo(rec.ProjectID, taskID, nextTaskID)
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next_task_id": nextTaskID})
		} else {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	action := parts[1]
	switch action {
	case "read":
		if req.Method == http.MethodPost {
			if !tasks.TaskMarkRead(taskID) {
				writeError(w, http.StatusNotFound, "任务不存在")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	case "stop":
		if req.Method == http.MethodPost {
			ok, detail := tasks.TaskStop(taskID)
			if !ok {
				writeError(w, http.StatusNotFound, detail)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	case "rename":
		if req.Method == http.MethodPost {
			var in struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(req.Body).Decode(&in)
			ok, detail := tasks.TaskRename(taskID, in.Name)
			if !ok {
				writeError(w, http.StatusBadRequest, detail)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	default:
		writeError(w, http.StatusNotFound, "Not Found")
	}
}
