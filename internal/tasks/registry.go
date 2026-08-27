package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"aimidi/internal/config"
	"aimidi/internal/project"
)

const (
	StatusRunning           = "running"
	StatusNeedsConfirmation = "needs_confirmation"
	StatusCompleted         = "completed"
)

// TaskRecord 任务数据记录
type TaskRecord struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"project_id"`
	Name            string         `json:"name"`
	Legacy          bool           `json:"legacy"`
	Message         string         `json:"message"`
	Status          string         `json:"status"`
	CreatedAt       float64        `json:"created_at"`
	StartedAt       float64        `json:"started_at"`
	FinishedAt      float64        `json:"finished_at"`
	Read            bool           `json:"read"`
	QuestionID      *string        `json:"question_id"`
	PendingQuestion map[string]any `json:"pending_question"`
	ProjectName     string         `json:"project_name,omitempty"`
}

var (
	mu          sync.RWMutex
	taskList    = make(map[string]*TaskRecord)
	taskOrder   []string
	cancelFlags = make(map[string]bool)
	loaded      = false
)

func nowTs() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func getTasksFile() string {
	return filepath.Join(config.ProjectsDir, "tasks.json")
}

func ensureLoadedLocked() {
	if loaded {
		return
	}
	loaded = true

	tfile := getTasksFile()
	data, err := os.ReadFile(tfile)
	if err != nil {
		return
	}

	var root struct {
		Tasks []*TaskRecord `json:"tasks"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return
	}

	now := nowTs()
	for _, t := range root.Tasks {
		if t == nil || t.ID == "" {
			continue
		}
		/* 孤儿任务：项目目录已不存在（如旧版本删项目未清任务）。
		   丢弃之——否则进行中/待确认的孤儿会让任务面板每次启动自动弹出，
		   永久盖住档案库右侧按钮 */
		if _, err := os.Stat(filepath.Join(config.ProjectsDir, t.ProjectID)); err != nil {
			continue
		}
		if t.Status != StatusNeedsConfirmation {
			t.Status = StatusCompleted
			if t.FinishedAt == 0 {
				t.FinishedAt = now
			}
			t.QuestionID = nil
			t.PendingQuestion = nil
		}
		taskList[t.ID] = t
		taskOrder = append(taskOrder, t.ID)
	}
}

func saveLocked() {
	tfile := getTasksFile()
	_ = os.MkdirAll(filepath.Dir(tfile), 0755)

	var items []*TaskRecord
	for _, id := range taskOrder {
		if t, ok := taskList[id]; ok {
			items = append(items, t)
		}
	}

	data, err := json.MarshalIndent(map[string]any{"tasks": items}, "", "  ")
	if err != nil {
		return
	}

	tmp := tfile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		_ = os.Rename(tmp, tfile)
	}
}

func autoTitle(message string) string {
	lines := strings.Split(message, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			runes := []rune(l)
			if len(runes) > 20 {
				return string(runes[:20]) + "…"
			}
			return string(runes)
		}
	}
	return "新任务"
}

// TaskCreate 创建新任务
func TaskCreate(projectID, name string, legacy bool) TaskRecord {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	now := nowTs()
	tid := strings.ReplaceAll(uuid.New().String(), "-", "")
	if name == "" {
		name = "新任务"
	}

	rec := &TaskRecord{
		ID:         tid,
		ProjectID:  projectID,
		Name:       name,
		Legacy:     legacy,
		Message:    "",
		Status:     StatusCompleted,
		CreatedAt:  now,
		StartedAt:  now,
		FinishedAt: now,
		Read:       true,
	}

	taskList[tid] = rec
	taskOrder = append(taskOrder, tid)
	saveLocked()

	return *rec
}

// TaskEnsure 绑定或创建任务
func TaskEnsure(projectID string, taskID *string, message string) TaskRecord {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	var task *TaskRecord
	if taskID != nil && *taskID != "" {
		if t, ok := taskList[*taskID]; ok && t.ProjectID == projectID {
			task = t
		}
	}

	if task == nil {
		for _, id := range taskOrder {
			t := taskList[id]
			if t.ProjectID == projectID && t.Legacy {
				task = t
				break
			}
		}
	}

	if task == nil {
		now := nowTs()
		tid := strings.ReplaceAll(uuid.New().String(), "-", "")
		task = &TaskRecord{
			ID:         tid,
			ProjectID:  projectID,
			Name:       "新任务",
			Legacy:     true,
			Status:     StatusCompleted,
			CreatedAt:  now,
			StartedAt:  now,
			FinishedAt: now,
			Read:       true,
		}
		taskList[tid] = task
		taskOrder = append(taskOrder, tid)
	}

	if (task.Name == "" || task.Name == "新任务") && message != "" {
		task.Name = autoTitle(message)
	}
	if task.Message == "" && message != "" {
		task.Message = message
	}

	saveLocked()
	return *task
}

// TaskEnsureDefault 项目无任务时惰性创建默认 legacy 任务
func TaskEnsureDefault(projectID string) TaskRecord {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	for _, id := range taskOrder {
		t := taskList[id]
		if t.ProjectID == projectID && t.Legacy {
			return *t
		}
	}

	now := nowTs()
	tid := strings.ReplaceAll(uuid.New().String(), "-", "")
	task := &TaskRecord{
		ID:         tid,
		ProjectID:  projectID,
		Name:       "新任务",
		Legacy:     true,
		Status:     StatusCompleted,
		CreatedAt:  now,
		StartedAt:  now,
		FinishedAt: now,
		Read:       true,
	}
	taskList[tid] = task
	taskOrder = append(taskOrder, tid)
	saveLocked()
	return *task
}

// TaskMarkRunning 标记任务为运行中
func TaskMarkRunning(taskID string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	if t, ok := taskList[taskID]; ok {
		t.Status = StatusRunning
		t.QuestionID = nil
		t.PendingQuestion = nil
		// 复跑（如回答提问后恢复）时重置时间戳，避免残留上次的 finished_at
		t.StartedAt = nowTs()
		t.FinishedAt = 0
		// 新一轮开始即视为撤销上一轮的停止请求——否则手动停止一次后，
		// 同一任务的后续消息永远被「任务已停止」拦截
		delete(cancelFlags, taskID)
		saveLocked()
	}
}

// TaskMarkNeedsConfirmation 标记任务为等待用户确认
func TaskMarkNeedsConfirmation(projectID string, pending map[string]any) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	var runningTask *TaskRecord
	if tid, ok := pending["task_id"].(string); ok && tid != "" {
		if t, ok := taskList[tid]; ok && t.ProjectID == projectID {
			runningTask = t
		}
	}
	if runningTask == nil {
		for _, id := range taskOrder {
			t := taskList[id]
			if t.ProjectID == projectID && t.Status == StatusRunning {
				runningTask = t
				break
			}
		}
	}

	if runningTask != nil {
		runningTask.Status = StatusNeedsConfirmation
		if qid, ok := pending["question_id"].(string); ok {
			runningTask.QuestionID = &qid
		}
		runningTask.PendingQuestion = pending
		saveLocked()
	}
}

// TaskFinish 任务正常完成
func TaskFinish(taskID string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	if t, ok := taskList[taskID]; ok && t.Status == StatusRunning {
		t.Status = StatusCompleted
		t.FinishedAt = nowTs()
		t.Read = false
		saveLocked()
	}
}

// TaskForceComplete 强制将任务收尾为已完成
func TaskForceComplete(taskID string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	if t, ok := taskList[taskID]; ok {
		t.Status = StatusCompleted
		t.FinishedAt = nowTs()
		t.Read = false
		t.QuestionID = nil
		t.PendingQuestion = nil
		saveLocked()
	}
}

// TaskIsCancelled 检查任务是否收到停止信号
func TaskIsCancelled(taskID string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return cancelFlags[taskID]
}

// TaskStop 停止任务
func TaskStop(taskID string) (bool, string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	t, ok := taskList[taskID]
	if !ok {
		return false, "任务不存在"
	}

	cancelFlags[taskID] = true
	if t.Status == StatusNeedsConfirmation {
		t.Status = StatusCompleted
		t.FinishedAt = nowTs()
		t.Read = false
		t.QuestionID = nil
		t.PendingQuestion = nil
		saveLocked()
	}

	return true, ""
}

// TaskRename 重命名任务
func TaskRename(taskID, newName string) (bool, string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	t, ok := taskList[taskID]
	if !ok {
		return false, "任务不存在"
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return false, "任务名称不能为空"
	}

	t.Name = newName
	saveLocked()
	return true, ""
}

// TaskGet 返回任务记录副本（不存在返回 nil）
func TaskGet(taskID string) *TaskRecord {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	if t, ok := taskList[taskID]; ok {
		cp := *t
		return &cp
	}
	return nil
}

// TaskDelete 删除任务
func TaskDelete(taskID string) (bool, string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	t, ok := taskList[taskID]
	if !ok {
		return false, "任务不存在"
	}
	if t.Status != StatusCompleted {
		return false, "任务未完成（进行中或需要确认），无法删除"
	}

	pid := t.ProjectID
	legacy := t.Legacy

	delete(cancelFlags, taskID)
	delete(taskList, taskID)
	var newOrder []string
	for _, id := range taskOrder {
		if id != taskID {
			newOrder = append(newOrder, id)
		}
	}
	taskOrder = newOrder
	saveLocked()

	project.DeleteTaskHistory(pid, taskID, legacy)
	return true, ""
}

// TaskList 返回任务列表
func TaskList(projectID string) []TaskRecord {
	mu.Lock()
	ensureLoadedLocked()

	var res []TaskRecord
	for _, id := range taskOrder {
		if t, ok := taskList[id]; ok {
			if projectID == "" || t.ProjectID == projectID {
				res = append(res, *t)
			}
		}
	}
	mu.Unlock()

	projects := project.ListProjects()
	nameMap := make(map[string]string)
	for _, p := range projects {
		nameMap[p.ID] = p.Name
	}
	for i := range res {
		res[i].ProjectName = nameMap[res[i].ProjectID]
	}

	return res
}

// TaskMarkRead 标记任务为已读。保存走短防抖：整项目标记已读时前端会
// 逐任务调用，此前每个请求都全量重写 tasks.json 一次。
func TaskMarkRead(taskID string) bool {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	if t, ok := taskList[taskID]; ok && t.Status == StatusCompleted {
		t.Read = true
		scheduleSaveLocked()
		return true
	}
	return false
}

var saveTimer *time.Timer

// scheduleSaveLocked 防抖落盘（调用方需持有 mu）；关键状态转移
// （运行/待确认/停止）仍由各自的 saveLocked 立即写盘。
func scheduleSaveLocked() {
	if saveTimer != nil {
		return
	}
	saveTimer = time.AfterFunc(300*time.Millisecond, func() {
		mu.Lock()
		defer mu.Unlock()
		saveTimer = nil
		saveLocked()
	})
}

// TaskHasActive 项目是否有未完成任务
func TaskHasActive(projectID string) bool {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	for _, t := range taskList {
		if t.ProjectID == projectID && t.Status != StatusCompleted {
			return true
		}
	}
	return false
}

// TaskPurgeProject 删除项目时清除其全部任务（内存与磁盘）。
// 此前删项目不清任务：残留的进行中/待确认任务会让任务面板永久自动弹出。
func TaskPurgeProject(projectID string) {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	var newOrder []string
	for _, id := range taskOrder {
		if t, ok := taskList[id]; ok && t.ProjectID == projectID {
			delete(cancelFlags, id)
			delete(taskList, id)
		} else {
			newOrder = append(newOrder, id)
		}
	}
	taskOrder = newOrder
	saveLocked()
}

// TaskResumeByQuestion 根据提问 ID 查找需要确认的任务
func TaskResumeByQuestion(projectID, questionID string) *TaskRecord {
	mu.Lock()
	defer mu.Unlock()
	ensureLoadedLocked()

	for _, t := range taskList {
		if t.ProjectID == projectID && t.Status == StatusNeedsConfirmation && t.QuestionID != nil && *t.QuestionID == questionID {
			// 用户回答提问 = 明确要求继续：清除此前停止按钮留下的取消标记，
			// 否则恢复流会在 ChatStream 入口立即以「任务已停止」退出且回答丢失
			delete(cancelFlags, t.ID)
			cp := *t
			return &cp
		}
	}
	return nil
}
