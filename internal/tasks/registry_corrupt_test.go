package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

// TestCorruptTasksJsonBackedUpNotOverwritten tasks.json 损坏（如进程崩溃
// 截断）时：必须先备份原件、本进程拒绝落盘——否则首次 saveLocked（任意
// TaskCreate/TaskMarkRead 都会触发）就把可能半截可恢复的任务记录替换成
// 空列表，任务面板全空。
func TestCorruptTasksJsonBackedUpNotOverwritten(t *testing.T) {
	withTempProjectsDir(t)
	resetRegistry()

	tfile := getTasksFile()
	if err := os.MkdirAll(filepath.Dir(tfile), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	corrupt := `{"tasks":[{"id":"task-` // 半截 JSON：模拟崩溃截断
	if err := os.WriteFile(tfile, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}

	// 触发加载（内部 unmarshal 失败 → 备份 + tasksCorrupt 置位）
	_ = TaskList("proj-x")

	// 原路径已改名为备份文件，且备份文件存在
	if _, err := os.Stat(tfile); !os.IsNotExist(err) {
		t.Fatalf("期望损坏原件已被改名备份，tasks.json 仍存在")
	}
	matches, _ := filepath.Glob(tfile + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("期望恰好 1 个备份文件，实际 %d 个", len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil || string(data) != corrupt {
		t.Fatalf("备份内容应与损坏原件一致: err=%v data=%q", err, string(data))
	}

	// 损坏未恢复期间拒绝落盘：TaskCreate 不得在 tasks.json 重新生成空列表
	_ = TaskCreate("proj-x", "新任务", false)
	if _, err := os.Stat(tfile); !os.IsNotExist(err) {
		t.Fatalf("tasksCorrupt 置位后 saveLocked 不应落盘，tasks.json 却被重建")
	}

	// 模拟人工恢复 + 重启（重置内存态）：加载正常文件后落盘恢复。
	// 先建项目目录：加载流程会把项目目录已删除的任务当孤儿丢弃
	if err := os.MkdirAll(filepath.Join(config.ProjectsDir, "proj-x"), 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}
	resetRegistry()
	if err := os.WriteFile(tfile, []byte(`{"tasks":[{"id":"ok1","project_id":"proj-x","name":"t","legacy":true,"status":"completed","created_at":1,"started_at":1,"finished_at":1,"read":true}]}`), 0o644); err != nil {
		t.Fatalf("写入恢复文件失败: %v", err)
	}
	list := TaskList("proj-x")
	if len(list) != 1 || list[0].ID != "ok1" {
		t.Fatalf("恢复后应能正常加载任务: %v", list)
	}
	// 用 TaskRename 直接触发同步落盘（TaskMarkRead 走 300ms 防抖定时器，
	// 会在测试结束、Cleanup 恢复 ProjectsDir 之后才触发，与测试基建竞争）
	if ok, msg := TaskRename("ok1", "restored"); !ok {
		t.Fatalf("恢复后重命名任务失败: %s", msg)
	}
	if _, err := os.Stat(tfile); err != nil {
		t.Fatalf("恢复正常后应恢复落盘: %v", err)
	}
	if got := tasksJSONForTest(t); !strings.Contains(got, "ok1") {
		t.Fatalf("落盘内容应包含已恢复任务: %q", got)
	}
}

func tasksJSONForTest(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(getTasksFile())
	if err != nil {
		t.Fatalf("读取 tasks.json 失败: %v", err)
	}
	return string(data)
}
