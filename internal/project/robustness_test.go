package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

// ═══════════ index.json 损坏保护 ═══════════
// 回归背景：loadIndexLocked 损坏时返回 nil，写方不加区分地把 nil 落盘
// 成 {"projects":null}——一次崩溃截断就会把所有项目从索引中抹掉

func TestCorruptIndexIsBackedUpNotWiped(t *testing.T) {
	tmpDir := setupTestProjectDir(t)
	defer os.RemoveAll(tmpDir)
	ResetProjectsIndexCache()

	if err := os.MkdirAll(config.ProjectsDir, 0755); err != nil {
		t.Fatal(err)
	}
	idxFile := filepath.Join(config.ProjectsDir, "index.json")

	// 1. 读路径：损坏索引 → 空列表 + 备份，不落盘覆盖
	if err := os.WriteFile(idxFile, []byte("{corrupt json"), 0644); err != nil {
		t.Fatal(err)
	}
	if list := ListProjects(); len(list) != 0 {
		t.Fatalf("ListProjects on corrupt index = %+v, want empty", list)
	}
	matches, _ := filepath.Glob(idxFile + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 corrupt backup, got %v", matches)
	}

	// 2. 写路径（updateIndexEntry）：损坏时绝不基于空条目落盘
	ResetProjectsIndexCache()
	if err := os.WriteFile(idxFile, []byte("{corrupt json"), 0644); err != nil {
		t.Fatal(err)
	}
	updateIndexEntry("any-id", func(e *ProjectEntry) { e.Name = "hijacked" })
	if _, err := os.Stat(idxFile); err == nil {
		data, _ := os.ReadFile(idxFile)
		t.Fatalf("corrupt index was overwritten instead of skipped: %s", data)
	}

	// 3. 写路径（DeleteProject）：同样不落盘覆盖
	ResetProjectsIndexCache()
	if err := os.WriteFile(idxFile, []byte("{corrupt json"), 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := filepath.Glob(idxFile + ".corrupt-*")
	if err := DeleteProject("any-id"); err != nil {
		t.Fatalf("DeleteProject on corrupt index: %v", err)
	}
	if _, err := os.Stat(idxFile); err == nil {
		t.Fatal("DeleteProject overwrote corrupt index instead of backing it up")
	}
	after, _ := filepath.Glob(idxFile + ".corrupt-*")
	if len(after) != len(before)+1 {
		t.Fatalf("expected %d corrupt backups after delete (one new), got %d", len(before)+1, len(after))
	}
}

func TestHealthyIndexStillWorks(t *testing.T) {
	tmpDir := setupTestProjectDir(t)
	defer os.RemoveAll(tmpDir)
	ResetProjectsIndexCache()

	meta, err := CreateProject("健康索引")
	if err != nil {
		t.Fatal(err)
	}
	list := ListProjects()
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Fatalf("ListProjects = %+v, want entry %s", list, meta.ID)
	}
	updateIndexEntry(meta.ID, func(e *ProjectEntry) { e.Name = "改名后" })
	list = ListProjects()
	if len(list) != 1 || list[0].Name != "改名后" {
		t.Fatalf("updateIndexEntry lost: %+v", list)
	}
	if err := DeleteProject(meta.ID); err != nil {
		t.Fatal(err)
	}
	if list := ListProjects(); len(list) != 0 {
		t.Fatalf("ListProjects after delete = %+v, want empty", list)
	}
}

// ═══════════ 工作区不可达时禁止同步 ═══════════
// 回归背景：U 盘/网络盘未挂载时 ScanMidiFiles 返回空集，同步的
// "镜像有、工作区无 → 删除"阶段会把整个镜像连同 note_table 清空

func TestWorkspaceSyncSkippedWhenUnreachable(t *testing.T) {
	tmpDir := setupTestProjectDir(t)
	defer os.RemoveAll(tmpDir)
	ResetProjectsIndexCache()

	meta, err := CreateProject("工作区同步")
	if err != nil {
		t.Fatal(err)
	}
	wsDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "a.mid"), []byte("MThd\x00\x00\x00\x06"), 0644); err != nil {
		t.Fatal(err)
	}
	SetWorkspaceDir(meta.ID, wsDir)

	got := SyncWorkspaceToProjects(meta.ID, nil)
	mirrorA := filepath.Join(MidiDir(meta.ID), "a.mid")
	if _, err := os.Stat(mirrorA); err != nil {
		t.Fatalf("mirror not created: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("sync result = %+v, want 1 file", got)
	}

	// 模拟盘未挂载：工作区目录整体消失
	if err := os.RemoveAll(wsDir); err != nil {
		t.Fatal(err)
	}
	got2 := SyncWorkspaceToProjects(meta.ID, got)
	if _, err := os.Stat(mirrorA); err != nil {
		t.Fatalf("mirror file was wiped when workspace unreachable: %v", err)
	}
	if len(got2) != 1 || got2[0].Name != "a.mid" {
		t.Fatalf("manifest rewritten as empty on unreachable workspace: %+v", got2)
	}
}

// ═══════════ 回收站同名防覆盖 ═══════════
// 回归背景：os.Rename 在 Windows 上直接替换已存在目标——恢复回收站
// 会覆盖用户新建的同名文件，二次移入会覆盖更早的回收备份

func TestTrashNeverOverwritesExistingFiles(t *testing.T) {
	tmpDir := setupTestProjectDir(t)
	defer os.RemoveAll(tmpDir)
	ResetProjectsIndexCache()

	meta, err := CreateProject("回收站防覆盖")
	if err != nil {
		t.Fatal(err)
	}
	base := MidiDir(meta.ID)
	trash := TrashDir(meta.ID)

	// 移入 → 重建同名 → 再移入：trash 内必须两份都在
	if err := os.WriteFile(filepath.Join(base, "foo.mid"), []byte("VERSION-1"), 0644); err != nil {
		t.Fatal(err)
	}
	rel1 := MoveToTrash(meta.ID, MidiFileInfo{Name: "foo.mid", Path: filepath.Join(base, "foo.mid")})
	if rel1 == "" {
		t.Fatal("first MoveToTrash failed")
	}
	if err := os.WriteFile(filepath.Join(base, "foo.mid"), []byte("VERSION-2"), 0644); err != nil {
		t.Fatal(err)
	}
	rel2 := MoveToTrash(meta.ID, MidiFileInfo{Name: "foo.mid", Path: filepath.Join(base, "foo.mid")})
	if rel2 == "" || rel2 == rel1 {
		t.Fatalf("second MoveToTrash rel = %q, must be non-empty and distinct from %q", rel2, rel1)
	}
	v1, err := os.ReadFile(filepath.Join(trash, filepath.FromSlash(rel1)))
	if err != nil || string(v1) != "VERSION-1" {
		t.Fatalf("earlier trash backup clobbered: %s (%v)", v1, err)
	}
	v2, err := os.ReadFile(filepath.Join(trash, filepath.FromSlash(rel2)))
	if err != nil || string(v2) != "VERSION-2" {
		t.Fatalf("second trash copy wrong: %s (%v)", v2, err)
	}

	// 恢复时 base 已有同名新文件：不得覆盖，恢复件落唯一名
	if err := os.WriteFile(filepath.Join(base, "foo.mid"), []byte("LIVE"), 0644); err != nil {
		t.Fatal(err)
	}
	RestoreFromTrash(meta.ID, []string{rel1})
	live, err := os.ReadFile(filepath.Join(base, "foo.mid"))
	if err != nil || string(live) != "LIVE" {
		t.Fatalf("restore overwrote the live file: %s (%v)", live, err)
	}
	restored, err := os.ReadFile(filepath.Join(base, "foo (1).mid"))
	if err != nil || string(restored) != "VERSION-1" {
		t.Fatalf("restored file missing/renamed wrongly: %s (%v)", restored, err)
	}
}

// uniqueDstPath 自身的边界行为
func TestUniqueDstPath(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "song.mid")
	if err := os.WriteFile(existing, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := uniqueDstPath(filepath.Join(dir, "absent.mid")); !strings.HasSuffix(got, "absent.mid") {
		t.Fatalf("absent target should keep its name, got %q", got)
	}
	got := uniqueDstPath(existing)
	if got != filepath.Join(dir, "song (1).mid") {
		t.Fatalf("uniqueDstPath = %q, want song (1).mid", got)
	}
	if err := os.WriteFile(got, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got2 := uniqueDstPath(existing); got2 != filepath.Join(dir, "song (2).mid") {
		t.Fatalf("uniqueDstPath = %q, want song (2).mid", got2)
	}
}
