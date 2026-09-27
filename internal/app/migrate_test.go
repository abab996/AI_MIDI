package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyDataCopiesWithoutDeletingSource(t *testing.T) {
	newRoot := t.TempDir()
	oldRoot := t.TempDir()
	mustWrite(t, filepath.Join(oldRoot, "settings.json"), []byte(`{"theme":"dark"}`))
	mustWrite(t, filepath.Join(oldRoot, "projects", "p1", "arrangement.json"), []byte(`{"version":1}`))
	mustWrite(t, filepath.Join(oldRoot, "Library", "user", "note.md"), []byte("乐理"))
	mustWrite(t, filepath.Join(oldRoot, "Library", "soundfonts", "Piano.SF2"), []byte("sf2"))
	mustWrite(t, filepath.Join(oldRoot, "output", "ai_midi.log"), []byte("old"))

	got, err := MigrateLegacyData(newRoot, []string{oldRoot})
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if got.Files != 5 || got.SoundFonts != 1 || got.From != oldRoot {
		t.Fatalf("迁移计数不正确: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(oldRoot, "settings.json")); err != nil {
		t.Fatal("原目录应保留")
	}
	if !fileExists(filepath.Join(newRoot, "settings.json")) {
		t.Fatal("settings.json 应被复制")
	}

	again, err := MigrateLegacyData(newRoot, []string{oldRoot})
	if err != nil || again.Files != 0 {
		t.Fatalf("第二次应跳过: %+v err=%v", again, err)
	}

	announced, ok := TakeMigrationAnnouncement(newRoot)
	if !ok || announced.Files != 5 || announced.SoundFonts != 1 {
		t.Fatalf("应提示一次: ok=%v %+v", ok, announced)
	}
	if _, ok := TakeMigrationAnnouncement(newRoot); ok {
		t.Fatal("提示只应出现一次")
	}
}

func TestMigrateLegacyDataSkipsWhenSettingsExist(t *testing.T) {
	newRoot := t.TempDir()
	oldRoot := t.TempDir()
	mustWrite(t, filepath.Join(newRoot, "settings.json"), []byte(`{"ok":true}`))
	mustWrite(t, filepath.Join(oldRoot, "settings.json"), []byte(`{"theme":"dark"}`))
	mustWrite(t, filepath.Join(oldRoot, "projects", "p1", "a.txt"), []byte("x"))

	got, err := MigrateLegacyData(newRoot, []string{oldRoot})
	if err != nil || got.Files != 0 {
		t.Fatalf("已有设置时不应迁移: %+v err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "projects")); !os.IsNotExist(err) {
		t.Fatal("不应复制 projects")
	}
}

func TestMigrateLegacyDataDoesNotOverwrite(t *testing.T) {
	newRoot := t.TempDir()
	oldRoot := t.TempDir()
	mustWrite(t, filepath.Join(newRoot, "projects", "p1", "a.txt"), []byte("keep"))
	mustWrite(t, filepath.Join(oldRoot, "settings.json"), []byte(`{}`))
	mustWrite(t, filepath.Join(oldRoot, "projects", "p1", "a.txt"), []byte("old"))
	mustWrite(t, filepath.Join(oldRoot, "projects", "p1", "b.txt"), []byte("new"))

	got, err := MigrateLegacyData(newRoot, []string{oldRoot})
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if got.Files != 2 {
		t.Fatalf("应只复制缺失文件和 settings: %+v", got)
	}
	body, err := os.ReadFile(filepath.Join(newRoot, "projects", "p1", "a.txt"))
	if err != nil || string(body) != "keep" {
		t.Fatalf("已有文件被覆盖: %q err=%v", body, err)
	}
}

func TestMigrateLegacyDataIgnoresSameRoot(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "settings.json"), []byte(`{}`))
	// 目标已有 settings 会先返回。这里去掉目标标记，只验证同源被跳过：
	if err := os.Remove(filepath.Join(root, "settings.json")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "projects", "p1", "a.txt"), []byte("x"))
	got, err := MigrateLegacyData(root, []string{root})
	if err != nil || got.Files != 0 {
		t.Fatalf("同源不应复制: %+v err=%v", got, err)
	}
}

func mustWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
}
