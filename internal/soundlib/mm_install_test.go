package soundlib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"melodymate/internal/config"
)

// makeInstalledMM 在临时目录里造一个"已安装的 MM 插件"（**不碰真实的 Library/**：
// 上一轮的单测用 config.LibraryDir 依赖 CWD，在 internal/mcp/ 下建出了真目录）
func makeInstalledMM(t *testing.T, dir, id, name, version, authorType string, caps []string, hash string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "bin", "win-x64"), 0o755); err != nil {
		t.Fatal(err)
	}
	mf := map[string]any{
		"schema": 1, "id": id, "name": name, "version": version,
		"kind": "effect", "entry": "win-x64/x.dll", "api": 1,
		"capabilities": caps,
		"author":       map[string]any{"type": authorType},
	}
	if hash != "" {
		mf["hash"] = map[string]any{"algorithm": "sha256", "files": map[string]string{"bin/win-x64/x.dll": hash}}
	}
	raw, _ := json.Marshal(mf)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncInstalledMMRegisters(t *testing.T) {
	// 注册表落盘位置由 config.LibraryDir 决定：指向临时目录，测试绝不碰仓库
	// （上一版依赖 CWD，在 internal/soundlib/ 下建出了真 Library/）
	old := config.LibraryDir
	config.LibraryDir = t.TempDir()
	defer func() { config.LibraryDir = old }()

	dir := filepath.Join(t.TempDir(), "com.test.sync@1.0.0")
	makeInstalledMM(t, dir, "com.test.sync", "Sync Test", "1.0.0", "ai", []string{"files"}, strings.Repeat("a", 64))

	src, created, err := SyncInstalledMM(dir)
	if err != nil || !created {
		t.Fatalf("同步失败: %v created=%v", err, created)
	}
	if src.Kind != KindMM {
		t.Fatalf("kind = %q, want mm", src.Kind)
	}
	if src.Origin != "ai" || src.AI == nil {
		t.Fatalf("AI 生成应标 origin=ai 且带 AIInfo: %+v", src)
	}
	if len(src.Capabilities) != 1 || src.Capabilities[0] != "files" {
		t.Fatalf("能力清单未同步: %v", src.Capabilities)
	}
	if !strings.HasPrefix(src.Hash, "aaaa") {
		t.Fatalf("哈希未同步: %q", src.Hash)
	}
	if !strings.HasPrefix(src.Path, "/") && strings.Contains(src.Path, "..") {
		t.Fatalf("路径不该含 ..: %q", src.Path)
	}

	// 重复同步 → 更新而非新增
	if _, created2, err2 := SyncInstalledMM(dir); err2 != nil || created2 {
		t.Fatalf("重复同步应走更新: created=%v err=%v", created2, err2)
	}
	count := 0
	for _, v := range List(KindMM, "") {
		if strings.HasPrefix(v.ID, "com_test_sync@") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("注册表应有 1 条，实际 %d", count)
	}

	// 卸载清理
	if err := UninstallMMSource("com.test.sync"); err != nil {
		t.Fatalf("卸载同步失败: %v", err)
	}
	for _, v := range List(KindMM, "") {
		if strings.HasPrefix(v.ID, "com_test_sync@") {
			t.Fatalf("卸载后仍残留: %s", v.ID)
		}
	}
}

func TestInstalledMMInfoHuman(t *testing.T) {
	old := config.LibraryDir
	config.LibraryDir = t.TempDir()
	defer func() { config.LibraryDir = old }()

	dir := filepath.Join(t.TempDir(), "com.test.human@2.0.0")
	makeInstalledMM(t, dir, "com.test.human", "Human Fx", "2.0.0", "human", nil, "")
	info, err := InstalledMMInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info["ai"] != false || info["hashVerified"] != false {
		t.Fatalf("人类插件应 ai=false hashVerified=false: %+v", info)
	}
	if info["name"] != "Human Fx" || info["version"] != "2.0.0" {
		t.Fatalf("摘要字段不对: %+v", info)
	}
}
