package mm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pkgWithCaps(t *testing.T, caps []string) string {
	t.Helper()
	m := map[string]any{
		"schema": 1, "id": "com.test.caps", "name": "Caps Test", "version": "1.0.0",
		"kind": "effect", "entry": "win-x64/myfilter.dll", "api": 1, "capabilities": caps,
	}
	p := buildPkg(t, m, files(nil))
	return p
}

func TestInstallCapabilityGate(t *testing.T) {
	pkg := pkgWithCaps(t, []string{"files", "network"})
	dest := filepath.Join(t.TempDir(), "plugins")

	// 1) preview：不落盘，列出声明能力与缺失授权
	preview, err := ValidateAndPreview(pkg, dest, nil)
	if err != nil {
		t.Fatalf("preview 失败: %v", err)
	}
	if len(preview.Capabilities) != 2 || len(preview.MissingGrant) != 2 {
		t.Fatalf("preview 能力清单不对: %+v", preview)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("preview 不应落盘，实际有 %d 项", len(entries))
	}

	// 2) 未授权安装 → 拒绝且不落盘
	if _, err := Install(pkg, dest, nil); err == nil || !strings.Contains(err.Error(), "尚未授权") {
		t.Fatalf("未授权应被拒: %v", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("拒绝后不应落盘，实际有 %d 项", len(entries))
	}

	// 3) 多授权（授予未声明能力）→ 拒绝
	if _, err := Install(pkg, dest, []string{"files", "network", "midi_out"}); err == nil ||
		!strings.Contains(err.Error(), "未声明") {
		t.Fatalf("多授权应被拒: %v", err)
	}

	// 4) 全部授权 → 安装成功，文件落盘
	preview, err = Install(pkg, dest, []string{"files", "network"})
	if err != nil {
		t.Fatalf("授权后安装失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(preview.TargetDir, "manifest.json")); err != nil {
		t.Fatalf("安装后缺 manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(preview.TargetDir, "bin", "win-x64", "myfilter.dll")); err != nil {
		t.Fatalf("安装后缺原生库: %v", err)
	}

	// 5) 重复安装同版本 → 拒绝（不覆盖既有安装）
	if _, err := Install(pkg, dest, []string{"files", "network"}); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重复安装应被拒: %v", err)
	}

	// 6) 卸载：只允许删 plugins 目录下的路径
	if err := Uninstall(preview.TargetDir); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}
	if _, err := os.Stat(preview.TargetDir); !os.IsNotExist(err) {
		t.Fatalf("卸载后目录仍在")
	}
	if err := Uninstall(filepath.Join(t.TempDir(), "not-plugins")); err == nil {
		t.Fatalf("非 plugins 目录应拒绝删除")
	}
}

func TestInstallRejectsTraversalPackage(t *testing.T) {
	pkg := buildPkg(t, map[string]any{
		"schema": 1, "id": "com.test.evil", "name": "Evil", "version": "1.0.0",
		"kind": "effect", "entry": "win-x64/x.dll", "api": 1,
	}, map[string]string{"../escape.dll": "x", "bin/win-x64/myfilter.dll": "x"})
	dest := filepath.Join(t.TempDir(), "plugins")
	if _, err := Install(pkg, dest, nil); err == nil || !strings.Contains(err.Error(), "MM008") {
		t.Fatalf("路径穿越包应被拒: %v", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("拒绝后不应落盘")
	}
}
