package project

import (
	"os"
	"path/filepath"
	"testing"

	"aimidi/internal/config"
)

// TestCorruptHistoryBackedUpNotOverwritten history.json 损坏时必须先备份
// 原件：否则下一次 SaveHistory 会把可能半截可恢复的对话历史无声覆盖掉
// （与 index.json 损坏策略一致）。
func TestCorruptHistoryBackedUpNotOverwritten(t *testing.T) {
	orig := config.ProjectsDir
	config.ProjectsDir = t.TempDir()
	t.Cleanup(func() { config.ProjectsDir = orig })

	const projectID = "proj-corrupt-hist"
	hfile := HistoryFile(projectID, "", true) // legacy 任务 → projects/<id>/history.json
	if err := os.MkdirAll(filepath.Dir(hfile), 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}
	corrupt := `{"messages":[{"role":"user","con` // 半截 JSON：模拟崩溃截断
	if err := os.WriteFile(hfile, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}

	messages, _ := LoadHistory(projectID, "", true)
	if len(messages) != 0 {
		t.Fatalf("损坏文件应按空历史处理，实际 %d 条", len(messages))
	}

	// 原路径已改名备份，备份内容与原件一致
	if _, err := os.Stat(hfile); !os.IsNotExist(err) {
		t.Fatalf("期望损坏原件已被改名备份，history.json 仍存在")
	}
	matches, _ := filepath.Glob(hfile + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("期望恰好 1 个备份文件，实际 %d 个", len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil || string(data) != corrupt {
		t.Fatalf("备份内容应与损坏原件一致: err=%v data=%q", err, string(data))
	}

	// edit_history.json 同款保护
	efile := EditHistoryFile(projectID, "", true)
	if err := os.WriteFile(efile, []byte(`{"edit_history":[{`), 0o644); err != nil {
		t.Fatalf("写入损坏编辑历史失败: %v", err)
	}
	_ = LoadEditHistory(projectID, "", true)
	if _, err := os.Stat(efile); !os.IsNotExist(err) {
		t.Fatalf("期望损坏的编辑历史已被改名备份")
	}
	ematches, _ := filepath.Glob(efile + ".corrupt-*")
	if len(ematches) != 1 {
		t.Fatalf("期望编辑历史恰好 1 个备份文件，实际 %d 个", len(ematches))
	}
}
