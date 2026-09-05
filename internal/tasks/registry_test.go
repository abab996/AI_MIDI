package tasks

import (
	"testing"

	"aimidi/internal/config"
)

// resetRegistry 清空注册表内存态，避免测试间与磁盘残留互相影响
// （测试与生产代码同包，可直接重置）。
func resetRegistry() {
	mu.Lock()
	defer mu.Unlock()
	taskList = make(map[string]*TaskRecord)
	taskOrder = nil
	cancelFlags = make(map[string]bool)
	cancelChans = make(map[string]chan struct{})
	loaded = false
	tasksCorrupt = false
}

func withTempProjectsDir(t *testing.T) {
	t.Helper()
	orig := config.ProjectsDir
	config.ProjectsDir = t.TempDir()
	t.Cleanup(func() { config.ProjectsDir = orig })
}

// TestTaskMarkRunningClosesOldCancelChan 同任务复跑（如回答提问后恢复）时，
// 上一轮的停止信号 chan 必须被关闭——否则旧 watcher 永远监听一个"没人会
// 关"的 chan，卡死的流无法被 TaskStop 打断，项目对话锁被永久占住。
func TestTaskMarkRunningClosesOldCancelChan(t *testing.T) {
	withTempProjectsDir(t)
	resetRegistry()

	rec := TaskEnsure("proj-cancel", nil, "hi")
	tid := rec.ID
	TaskMarkRunning(tid)

	oldCh := TaskCancelChan(tid)
	if oldCh == nil {
		t.Fatalf("期望 TaskMarkRunning 后存在取消 chan")
	}

	// 同任务复跑：旧 chan 必须被关闭，且替换为新 chan
	TaskMarkRunning(tid)
	newCh := TaskCancelChan(tid)
	if newCh == nil {
		t.Fatalf("期望复跑后存在新的取消 chan")
	}
	if newCh == oldCh {
		t.Fatalf("期望复跑后替换为新 chan，实际仍为旧 chan")
	}
	select {
	case _, ok := <-oldCh:
		if ok {
			t.Fatalf("期望旧 chan 已关闭，实际仍开放")
		}
	default:
		t.Fatalf("期望旧 chan 已关闭（立即收到关闭信号），实际阻塞")
	}
}

// TestTaskStopReRunCycleNoPanic TaskStop（关闭+删除）与 TaskMarkRunning
// （重建）交替不应重复关闭 chan 而 panic。
func TestTaskStopReRunCycleNoPanic(t *testing.T) {
	withTempProjectsDir(t)
	resetRegistry()

	rec := TaskEnsure("proj-cycle", nil, "x")
	tid := rec.ID
	for i := 0; i < 3; i++ {
		TaskMarkRunning(tid)
		if stopped, _ := TaskStop(tid); !stopped {
			t.Fatalf("第 %d 轮 TaskStop 期望成功", i+1)
		}
	}
}
