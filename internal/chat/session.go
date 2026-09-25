package chat

import (
	"context"
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
	sessionsMu sync.RWMutex
	sessions   = make(map[string]*ProjectSession)
	// 项目级对话锁用容量 1 的 chan 信号量（区别于 *sync.Mutex）：等锁期间
	// 可以 select ctx.Done() 响应任务停止。否则上游卡死持锁时，同项目的
	// 后续消息会永久阻塞在锁获取上（表现为一直转圈、零事件、只能重进）
	sessionChatLock = make(map[string]chan struct{})
	chatLockGuard   sync.Mutex
	sessionFileLock = make(map[string]*sync.RWMutex)
	fileLockGuard   sync.Mutex
)

// GetProjectChatLock 获取项目级对话锁信号量（保证同一项目多轮会话有序，不同项目完全并发）
func GetProjectChatLock(projectID string) chan struct{} {
	chatLockGuard.Lock()
	defer chatLockGuard.Unlock()
	l, ok := sessionChatLock[projectID]
	if !ok {
		l = make(chan struct{}, 1)
		l <- struct{}{}
		sessionChatLock[projectID] = l
	}
	return l
}

// AcquireProjectChatLock 等待项目对话锁；ctx 取消（用户停止任务）时立即返回
// 错误，不再无限排队。持锁期间必须调用 ReleaseProjectChatLock 归还。
func AcquireProjectChatLock(ctx context.Context, projectID string) error {
	select {
	case <-GetProjectChatLock(projectID):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseProjectChatLock 归还项目对话锁（仅持锁者调用；非阻塞，重复释放安全）
func ReleaseProjectChatLock(projectID string) {
	select {
	case GetProjectChatLock(projectID) <- struct{}{}:
	default:
	}
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

// WithSessionLock 在项目文件锁保护下执行 fn。defer 释放保证 fn 内 panic
// 时锁不会被永久占住（此前 pipeline 里 fl.Lock()...fl.Unlock() 若在锁内
// panic，锁永不释放，同项目对话后续永久卡死、只能重启应用）；panic 本身
// 继续向上传播，由调用方的 recover 兜底。
func WithSessionLock(projectID string, fn func()) {
	fl := GetSessionLock(projectID)
	fl.Lock()
	defer fl.Unlock()
	fn()
}

// SnapshotDisplay 逐条浅拷贝 ChatDisplay（每条消息复制为独立 map）。
// 调用方须已持有对应项目的会话读锁；拷贝出来的切片可安全地在锁外
// 序列化/返回给 HTTP 响应，不会与流式写入方竞争。
func SnapshotDisplay(display []map[string]any) []map[string]any {
	if display == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(display))
	for _, m := range display {
		if m == nil {
			continue // 防御：nil 条目序列化为 JSON null，前端读取字段会崩
		}
		cp := make(map[string]any, len(m))
		for k, v := range m {
			cp[k] = v
		}
		out = append(out, cp)
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

// CloneHistory 深拷贝历史切片（含每条消息内嵌套的 map/slice）。
// 快照场景（PendingEdit、撤销栈等）必须存深拷贝而非切片引用：
// 截断（FullHistory[:histIdx+1]）后继续 append 会原地覆写底层数组
// 槽位，嵌套 map 也会被流式写入就地修改，浅引用快照恢复出来的是
// 被污染的数据（表现为撤回后历史损坏、消息丢失）。
func CloneHistory(history []map[string]any) []map[string]any {
	if history == nil {
		return nil
	}
	out := make([]map[string]any, len(history))
	for i, m := range history {
		out[i] = cloneMsgMap(m)
	}
	return out
}

// CloneDisplay 深拷贝显示列表（含 questions/tool_calls 等嵌套结构）
func CloneDisplay(display []map[string]any) []map[string]any {
	return CloneHistory(display)
}

func cloneMsgMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = deepCopyAny(v)
	}
	return cp
}

// deepCopyAny 递归拷贝 JSON 兼容值；标量（string/数字/bool/nil）不可变，共享安全
func deepCopyAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMsgMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopyAny(e)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(t))
		for i, e := range t {
			out[i] = cloneMsgMap(e)
		}
		return out
	case []string:
		out := make([]string, len(t))
		copy(out, t)
		return out
	default:
		return v
	}
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
				if st.ChatDisplay[i] == nil {
					continue // 历史持久化为 null 的条目：读取安全，写入会 panic
				}
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
		// 恢复"上次打开的任务"：取最近活跃（StartedAt 最大）的任务，
		// 而非固定的 legacy 默认任务——重启后回到用户离开时的上下文
		currentTask := tasks.TaskLatestActive(projectID)
		if currentTask == "" {
			currentTask = defTask.ID
		}

		s = &ProjectSession{
			ProjectID:     projectID,
			CurrentTaskID: currentTask,
			TaskStates:    make(map[string]*TaskState),
			MidiFiles:     midiFiles,
		}
		s.TaskStates[currentTask] = loadTaskState(projectID, currentTask)
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
