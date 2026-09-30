package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestValidateMmPluginPath 原生库路径必须落在已安装插件目录内。
// 引擎对 format=mm 的 path 直接 LoadLibrary，目录约束只能在信任方强制。
func TestValidateMmPluginPath(t *testing.T) {
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	pluginDir := filepath.Join(plugins, "com.example.verb@0.1.0")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	inside := filepath.Join(pluginDir, "verb.dll")
	writeFile(t, inside, "not-a-real-dll")
	writeFile(t, filepath.Join(root, "evil.dll"), "outside")

	if err := ValidateMmPluginPath(plugins, inside); err != nil {
		t.Fatalf("插件目录内的库应被接受: %v", err)
	}

	if err := ValidateMmPluginPath(plugins, filepath.Join(root, "evil.dll")); err == nil {
		t.Fatalf("插件目录外的库应被拒绝")
	}
	// .. 穿越回退到目录外
	if err := ValidateMmPluginPath(plugins, filepath.Join(pluginDir, "..", "..", "evil.dll")); err == nil {
		t.Fatalf(".. 穿越应被拒绝")
	}
	// 未配置插件目录：fail-closed
	if err := ValidateMmPluginPath("", inside); err == nil {
		t.Fatalf("未配置插件目录时应拒绝")
	}
	// 不存在的库：拒绝（不给引擎"稍后创建"的机会）
	if err := ValidateMmPluginPath(plugins, filepath.Join(pluginDir, "missing.dll")); err == nil {
		t.Fatalf("不存在的库应被拒绝")
	}
}

// TestValidateMmPluginPathRejectsSymlinkEscape 目录联接/符号链接不能把加载
// 带出插件目录（Windows 上创建符号链接需要权限，失败则跳过）。
func TestValidateMmPluginPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		// 普通权限下 os.Symlink 会失败，跳过而不是误报通过
		if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
			t.Skipf("当前环境无法创建符号链接（%v），跳过", err)
		}
	}

	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	writeFile(t, filepath.Join(outside, "evil.dll"), "outside")

	link := filepath.Join(plugins, "escaped")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("无法创建符号链接（%v），跳过", err)
	}

	err := ValidateMmPluginPath(plugins, filepath.Join(link, "evil.dll"))
	if err == nil {
		t.Fatalf("经符号链接指向目录外的库应被拒绝")
	}
	if !strings.Contains(err.Error(), "越出") && !strings.Contains(err.Error(), "不可解析") {
		t.Logf("拒绝原因（可接受）: %v", err)
	}
}

// TestValidateScanDirs 扫描目录必须是存在的绝对目录；空列表 = 系统默认目录。
func TestValidateScanDirs(t *testing.T) {
	dir := t.TempDir()
	if err := validateScanDirs(nil); err != nil {
		t.Fatalf("空列表应被接受（系统默认目录）: %v", err)
	}
	if err := validateScanDirs([]string{dir}); err != nil {
		t.Fatalf("存在的绝对目录应被接受: %v", err)
	}
	if err := validateScanDirs([]string{filepath.Join(dir, "nope")}); err == nil {
		t.Fatalf("不存在的目录应被拒绝")
	}
	if err := validateScanDirs([]string{"relative/dir"}); err == nil {
		t.Fatalf("相对路径应被拒绝")
	}
	file := filepath.Join(dir, "a.txt")
	writeFile(t, file, "x")
	if err := validateScanDirs([]string{file}); err == nil {
		t.Fatalf("文件（非目录）应被拒绝")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}
