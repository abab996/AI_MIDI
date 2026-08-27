package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"aimidi/internal/config"
)

func TestRestoreAICreatedFiles(t *testing.T) {
	// 创建临时项目目录
	tmpDir, err := os.MkdirTemp("", "project_restore_test_*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	projectID := "test_proj_restore"
	projDir := filepath.Join(tmpDir, projectID)
	midiDir := filepath.Join(projDir, "midi")
	_ = os.MkdirAll(midiDir, 0755)

	origProjectsDir := config.ProjectsDir
	config.ProjectsDir = tmpDir
	defer func() { config.ProjectsDir = origProjectsDir }()

	// 1. 初始化文件
	midiFile := filepath.Join(midiDir, "test.mid")
	if err := os.WriteFile(midiFile, []byte("dummy midi content"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	initialFiles := []MidiFileInfo{
		{Name: "test.mid", Path: midiFile, Size: 18},
	}
	SaveMidiManifest(projectID, initialFiles)

	entries := map[string][]string{
		"files": []string{"test.mid"},
		"dirs":  []string{},
	}

	// 2. 撤回修改 (移入回收站)
	kept, rollback := RemoveAICreatedFiles(projectID, initialFiles, entries)
	if len(kept) != 0 {
		t.Fatalf("期望 kept 为空，实际: %d", len(kept))
	}
	if _, err := os.Stat(midiFile); !os.IsNotExist(err) {
		t.Fatalf("原文件应当已被移入回收站")
	}

	// 3. 放弃撤回 (恢复文件) - 测试 []string 类型
	restored := RestoreAICreatedFiles(projectID, rollback)
	if len(restored) == 0 {
		t.Fatalf("期望恢复后文件列表不为空")
	}
	if _, err := os.Stat(midiFile); err != nil {
		t.Fatalf("原文件应当已被恢复: %v", err)
	}

	// 4. 再次撤回并模拟从 JSON 反序列化得到的 rollback（[]any 类型）
	_, rollback2 := RemoveAICreatedFiles(projectID, restored, entries)
	rollbackJSON, err := json.Marshal(rollback2)
	if err != nil {
		t.Fatalf("序列化 rollback 失败: %v", err)
	}
	var rollbackUnmarshaled map[string]any
	if err := json.Unmarshal(rollbackJSON, &rollbackUnmarshaled); err != nil {
		t.Fatalf("反序列化 rollback 失败: %v", err)
	}

	restoredFromJSON := RestoreAICreatedFiles(projectID, rollbackUnmarshaled)
	if len(restoredFromJSON) == 0 {
		t.Fatalf("期望通过反序列化 rollback 恢复后文件列表不为空")
	}
	if _, err := os.Stat(midiFile); err != nil {
		t.Fatalf("原文件应当在反序列化 rollback 恢复后存在: %v", err)
	}
	manifestFiles := LoadMidiManifest(projectID)
	if len(manifestFiles) == 0 {
		t.Fatalf("期望 manifest 中的文件也被恢复保存")
	}
}
