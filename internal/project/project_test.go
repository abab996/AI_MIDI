package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

func setupTestProjectDir(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "aimidi_proj_test_*")
	if err != nil {
		t.Fatal(err)
	}
	config.ProjectsDir = filepath.Join(tmpDir, "projects")
	return tmpDir
}

func TestProjectLifecycle(t *testing.T) {
	tmpDir := setupTestProjectDir(t)
	defer os.RemoveAll(tmpDir)

	// 1. Create
	meta, err := CreateProject("测试项目一")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}
	if meta.Name != "测试项目一" || meta.ID == "" {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	// 2. List
	list := ListProjects()
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Fatalf("ListProjects got %+v, want 1 entry with id %s", list, meta.ID)
	}

	// 3. Rename
	err = RenameProject(meta.ID, "重命名项目")
	if err != nil {
		t.Fatalf("RenameProject failed: %v", err)
	}
	updated, err := LoadProject(meta.ID)
	if err != nil || updated.Name != "重命名项目" {
		t.Fatalf("LoadProject after rename got %+v", updated)
	}

	// 4. Save History & Manifest
	messages := []map[string]any{
		{"role": "user", "content": "帮我写一首 C 大调旋律"},
		{"role": "assistant", "content": "好的，这是 C 大调旋律"},
	}
	midiFiles := []MidiFileInfo{
		{Name: "melody.mid", Path: filepath.Join(MidiDir(meta.ID), "melody.mid"), Size: 1024},
	}
	SaveHistory(meta.ID, messages, midiFiles, "", true)

	loadedMsgs, loadedMidis := LoadHistory(meta.ID, "", true)
	if len(loadedMsgs) != 2 {
		t.Errorf("loadedMsgs count = %d, want 2", len(loadedMsgs))
	}
	if len(loadedMidis) != 1 || loadedMidis[0].Name != "melody.mid" {
		t.Errorf("loadedMidis = %+v", loadedMidis)
	}

	// 5. Draft
	SaveDraft(meta.ID, "暂存的草稿内容")
	if draft := LoadDraft(meta.ID); draft != "暂存的草稿内容" {
		t.Errorf("LoadDraft = %q, want '暂存的草稿内容'", draft)
	}

	// 6. Search
	results, ids := SearchProjects("C 大调")
	if len(results) == 0 || len(ids) == 0 || ids[0] != meta.ID {
		t.Errorf("SearchProjects('C 大调') failed, results=%v, ids=%v", results, ids)
	}

	// 验证密集中文与多字节字符切片安全
	chineseText := "这是一段非常非常非常非常非常非常非常非常长的中文字符串用于测试五度圈乐理与转调技巧和副歌旋律"
	SaveHistory(meta.ID, []map[string]any{
		{"role": "user", "content": chineseText},
	}, midiFiles, "", true)
	resultsCN, _ := SearchProjects("五度圈")
	if len(resultsCN) == 0 || len(resultsCN[0]) < 2 {
		t.Fatalf("SearchProjects('五度圈') failed, got %+v", resultsCN)
	}
	snippet := resultsCN[0][1]
	if !strings.Contains(snippet, "五度圈") {
		t.Fatalf("SearchProjects snippet does not contain keyword: %s", snippet)
	}

	// 7. Copy
	copied, err := CopyProject(meta.ID, "复制的项目")
	if err != nil {
		t.Fatalf("CopyProject failed: %v", err)
	}
	if copied.ID == meta.ID || copied.Name != "复制的项目" {
		t.Fatalf("CopyProject returned invalid meta: %+v", copied)
	}

	// 8. Path Traversal Defense
	_, err = ResolveMidiPath(meta.ID, "../secret.txt")
	if err == nil {
		t.Errorf("ResolveMidiPath with ../ should fail")
	}
	_, err = ResolveMidiPath(meta.ID, "C:\\Windows\\System32")
	if err == nil {
		t.Errorf("ResolveMidiPath with abs path should fail")
	}
	_, err = ResolveMidiPath(meta.ID, "~/bashrc")
	if err == nil {
		t.Errorf("ResolveMidiPath with ~ should fail")
	}

	// 9. Delete
	if err := DeleteProject(meta.ID); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}
	remList := ListProjects()
	if len(remList) != 1 || remList[0].ID != copied.ID {
		t.Fatalf("ListProjects after delete got %+v", remList)
	}
}

func TestProjectGlobalBPM(t *testing.T) {
	tmpDir := setupTestProjectDir(t)
	defer os.RemoveAll(tmpDir)

	meta, err := CreateProject("BPM测试项目")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	// 未设置时回退默认 120
	if got := GetProjectBPM(meta.ID); got != DefaultBPM {
		t.Fatalf("default BPM = %d, want %d", got, DefaultBPM)
	}

	// 写入后可读回
	SaveProjectBPM(meta.ID, 96)
	if got := GetProjectBPM(meta.ID); got != 96 {
		t.Fatalf("BPM after save = %d, want 96", got)
	}

	// SaveMidiManifest 不得覆盖 bpm 字段
	files := []MidiFileInfo{{Name: "a.mid", Path: "x/a.mid", Size: 10}}
	SaveMidiManifest(meta.ID, files)
	if got := GetProjectBPM(meta.ID); got != 96 {
		t.Fatalf("BPM after manifest save = %d, want 96 (manifest must preserve bpm)", got)
	}
	loaded := LoadMidiManifest(meta.ID)
	if len(loaded) != 1 || loaded[0].Name != "a.mid" {
		t.Fatalf("manifest files lost after bpm save: %+v", loaded)
	}

	// 非法值拒绝写入
	SaveProjectBPM(meta.ID, 9999)
	if got := GetProjectBPM(meta.ID); got != 96 {
		t.Fatalf("out-of-range BPM should be rejected, got %d", got)
	}
}
