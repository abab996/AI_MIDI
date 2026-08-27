package chat

import (
	"sync"

	"aimidi/internal/project"
	"aimidi/internal/tasks"
)

// UndoEntry 撤销栈快照项
type UndoEntry struct {
	Files []project.MidiFileInfo `json:"files"`
	Trash []string               `json:"trash"`
	Moves []any                  `json:"moves,omitempty"`
}

// TaskState 任务专属的会话状态
type TaskState struct {
	FullHistory     []map[string]any `json:"full_history"`
	ChatDisplay     []map[string]any `json:"chat_display"`
	UndoStack       []UndoEntry      `json:"undo_stack"`
	PendingEdit     map[string]any   `json:"pending_edit"`
	EditHistory     []map[string]any `json:"edit_history"`
	PendingQuestion map[string]any   `json:"pending_question"`
}

// ProjectSession 项目级会话状态（MIDI 清单项目级共享，多任务各自隔离）
type ProjectSession struct {
	mu            sync.RWMutex
	ProjectID     string
	CurrentTaskID string
	TaskStates    map[string]*TaskState
	MidiFiles     []project.MidiFileInfo
}

var (
	sessionsMu      sync.RWMutex
	sessions        = make(map[string]*ProjectSession)
	sessionChatLock = make(map[string]*sync.Mutex)
	chatLockGuard   sync.Mutex
	sessionFileLock = make(map[string]*sync.RWMutex)
	fileLockGuard   sync.Mutex
)

// GetProjectChatLock 获取项目级对话锁（保证同一项目多轮会话有序，不同项目完全并发）
func GetProjectChatLock(projectID string) *sync.Mutex {
	chatLockGuard.Lock()
	defer chatLockGuard.Unlock()
	l, ok := sessionChatLock[projectID]
	if !ok {
		l = &sync.Mutex{}
		sessionChatLock[projectID] = l
	}
	return l
}

// GetSessionLock 获取项目文件状态锁
func GetSessionLock(projectID string) *sync.RWMutex {
	fileLockGuard.Lock()
	defer fileLockGuard.Unlock()
	l, ok := sessionFileLock[projectID]
	if !ok {
		l = &sync.RWMutex{}
		sessionFileLock[projectID] = l
	}
	return l
}

// SnapshotDisplay 逐条浅拷贝 ChatDisplay（每条消息复制为独立 map）。
// 调用方须已持有对应项目的会话读锁；拷贝出来的切片可安全地在锁外
// 序列化/返回给 HTTP 响应，不会与流式写入方竞争。
func SnapshotDisplay(display []map[string]any) []map[string]any {
	if display == nil {
		return nil
	}
	out := make([]map[string]any, len(display))
	for i, m := range display {
		cp := make(map[string]any, len(m))
		for k, v := range m {
			cp[k] = v
		}
		out[i] = cp
	}
	return out
}

// SnapshotMidiFiles 拷贝 MIDI 清单切片（元素为值结构体，直接复制即一致快照）。
// 调用方须已持有对应项目的会话读锁。
func SnapshotMidiFiles(files []project.MidiFileInfo) []project.MidiFileInfo {
	if files == nil {
		return nil
	}
	out := make([]project.MidiFileInfo, len(files))
	copy(out, files)
	return out
}

func loadTaskState(projectID, taskID string) *TaskState {
	taskRec := tasks.TaskEnsure(projectID, &taskID, "")
	legacy := taskRec.Legacy

	fullHistory, _ := project.LoadHistory(projectID, taskID, legacy)
	display := project.RebuildDisplayFromHistory(fullHistory)
	editHist := project.LoadEditHistory(projectID, taskID, legacy)

	st := &TaskState{
		FullHistory: fullHistory,
		ChatDisplay: display,
		UndoStack:   nil,
		EditHistory: editHist,
	}

	if taskRec.Status == tasks.StatusNeedsConfirmation && taskRec.PendingQuestion != nil {
		st.PendingQuestion = taskRec.PendingQuestion
		// 重建显示列表已由 ask 调用生成 pending 提问卡片（占位
		// question_id=tool_call_id）——这里回填真实 question_id 供前端
		// 提交回答时匹配；找不到可回填的卡片时才兜底追加，避免重复
		realQid, _ := taskRec.PendingQuestion["question_id"].(string)
		replaced := false
		if realQid != "" {
			for i := len(st.ChatDisplay) - 1; i >= 0; i-- {
				if t, _ := st.ChatDisplay[i]["type"].(string); t != "question" {
					continue
				}
				if s, _ := st.ChatDisplay[i]["status"].(string); s == "pending" {
					st.ChatDisplay[i]["question_id"] = realQid
					st.ChatDisplay[i]["questions"] = NormalizeQuestions(taskRec.PendingQuestion["questions"])
					replaced = true
				}
				break // 只处理最后一张提问卡（未回答的提问只可能是最后一张）
			}
		}
		if !replaced {
			st.ChatDisplay = append(st.ChatDisplay, map[string]any{
				"role":        "assistant",
				"type":        "question",
				"question_id": realQid,
				"questions":   NormalizeQuestions(taskRec.PendingQuestion["questions"]),
				"status":      "pending",
			})
		}
	}

	return st
}

// GetSession 获取或从磁盘水合项目会话
func GetSession(projectID string) *ProjectSession {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	s, ok := sessions[projectID]
	if !ok {
		midiFiles := project.LoadMidiManifest(projectID)
		defTask := tasks.TaskEnsureDefault(projectID)

		s = &ProjectSession{
			ProjectID:     projectID,
			CurrentTaskID: defTask.ID,
			TaskStates:    make(map[string]*TaskState),
			MidiFiles:     midiFiles,
		}
		s.TaskStates[defTask.ID] = loadTaskState(projectID, defTask.ID)
		sessions[projectID] = s
	}
	return s
}

// SwitchTask 切换当前活跃任务
func (s *ProjectSession) SwitchTask(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.CurrentTaskID == taskID {
		return
	}
	if _, ok := s.TaskStates[taskID]; !ok {
		s.TaskStates[taskID] = loadTaskState(s.ProjectID, taskID)
	}
	s.CurrentTaskID = taskID
}

// GetTaskState 获取指定任务的独立状态对象
func (s *ProjectSession) GetTaskState(taskID string) *TaskState {
	s.mu.Lock()
	defer s.mu.Unlock()

	if taskID == "" {
		taskID = s.CurrentTaskID
	}
	st, ok := s.TaskStates[taskID]
	if !ok {
		st = loadTaskState(s.ProjectID, taskID)
		s.TaskStates[taskID] = st
	}
	return st
}

// DropTaskState 丢弃已删除任务的状态
func DropTaskState(projectID, taskID string) {
	DropTaskStateTo(projectID, taskID, "")
}

// DropTaskStateTo 丢弃已删除任务的状态并切换当前任务。
// nextTaskID 非空时切到它（删除前由调用方挑选：优先被删任务在创建序中
// 的前一个）；为空时自动挑选剩余任务，一个不剩则新建默认任务。
func DropTaskStateTo(projectID, taskID, nextTaskID string) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

	if s, ok := sessions[projectID]; ok {
		s.mu.Lock()
		delete(s.TaskStates, taskID)
		if s.CurrentTaskID == taskID {
			if nextTaskID == "" {
				rem := tasks.TaskList(projectID)
				if len(rem) > 0 {
					nextTaskID = rem[0].ID
				} else {
					nextTaskID = tasks.TaskEnsureDefault(projectID).ID
				}
			}
			s.CurrentTaskID = nextTaskID
		}
		s.mu.Unlock()
	}
}

// DropSession 丢弃整个项目会话
func DropSession(projectID string) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	delete(sessions, projectID)
}
