package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

// withTempLibrary 临时把 LibraryDir/LibraryUserDir 换绑到独立目录。
// 两个路径都是包级变量，后者不会随前者自动重算，必须一并替换——否则
// ListLibraryFiles 会读到真实的 Library/user，测试结果随本机状态漂移
func withTempLibrary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldLib, oldUser := config.LibraryDir, config.LibraryUserDir
	config.LibraryDir = dir
	config.LibraryUserDir = filepath.Join(dir, "user")
	t.Cleanup(func() { config.LibraryDir, config.LibraryUserDir = oldLib, oldUser })
	return dir
}

func writeLibFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadLibraryFileUnavailableDir 知识库目录整体缺失（典型：安装包漏装
// Library）时，错误必须明确提示「知识库不可用」并包含实际路径——此前
// 返回裸的「文件不存在 + 空可用列表」，故障无法定位。
func TestReadLibraryFileUnavailableDir(t *testing.T) {
	withTempLibrary(t) // user 目录同样指向空临时路径，保证「整体缺失」语义

	_, err := ReadLibraryFile("01_乐理基础.md")
	if err == nil {
		t.Fatalf("知识库目录缺失时应返回错误")
	}
	if !strings.Contains(err.Error(), "知识库不可用") {
		t.Fatalf("错误信息应提示知识库不可用: %v", err)
	}
	if !strings.Contains(err.Error(), config.LibraryDir) {
		t.Fatalf("错误信息应包含实际查找路径 %s: %v", config.LibraryDir, err)
	}
}

// TestReadLibraryFileNormalAndMissing 正常读取与「文件存在但目标缺失」两条路径。
func TestReadLibraryFileNormalAndMissing(t *testing.T) {
	dir := withTempLibrary(t)

	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# 知识"), 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	data, err := ReadLibraryFile("a.md")
	if err != nil || data != "# 知识" {
		t.Fatalf("正常读取失败: data=%q err=%v", data, err)
	}

	_, err = ReadLibraryFile("missing.md")
	if err == nil || !strings.Contains(err.Error(), "可用文件：a.md") {
		t.Fatalf("目标缺失时应列出可用文件: %v", err)
	}

	// 路径逃逸仍被拒绝
	if _, err := ReadLibraryFile("../escape.md"); err == nil {
		t.Fatalf("路径逃逸应被拒绝")
	}
}

// 用户自定义知识文件必须以 user/ 前缀进入清单（内置在前、用户文件在后），
// 非文本文件与子目录不收录
func TestListLibraryFilesIncludesUserDir(t *testing.T) {
	dir := withTempLibrary(t)

	writeLibFile(t, filepath.Join(dir, "01_乐理基础.md"), "x")
	writeLibFile(t, filepath.Join(dir, "备注.txt"), "x")
	writeLibFile(t, filepath.Join(dir, "sound.sf2"), "RIFF")
	writeLibFile(t, filepath.Join(config.LibraryUserDir, "爵士和声.txt"), "y")
	writeLibFile(t, filepath.Join(config.LibraryUserDir, "notes.md"), "y")
	// 子目录不递归
	writeLibFile(t, filepath.Join(config.LibraryUserDir, "nested", "deep.md"), "y")

	files := ListLibraryFiles()
	want := []string{"01_乐理基础.md", "备注.txt", "user/notes.md", "user/爵士和声.txt"}
	if len(files) != len(want) {
		t.Fatalf("ListLibraryFiles() = %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files[%d] = %q, want %q（完整列表 %v）", i, files[i], want[i], files)
		}
	}
}

// user/ 前缀路径可正常读取（用户自定义文件的 AI 读取路径）
func TestReadLibraryFileUserPath(t *testing.T) {
	withTempLibrary(t)
	writeLibFile(t, filepath.Join(config.LibraryUserDir, "我的知识.md"), "# 内容")

	got, err := ReadLibraryFile("user/我的知识.md")
	if err != nil {
		t.Fatalf("ReadLibraryFile(user/我的知识.md) 出错: %v", err)
	}
	if got != "# 内容" {
		t.Fatalf("内容 = %q, want %q", got, "# 内容")
	}
}

// 防线回归：非 .md/.txt 拒绝（Library 下还有 soundfonts 等二进制资源）、
// .. 逃逸拒绝、空名拒绝、不存在的文件报错
func TestReadLibraryFileRejects(t *testing.T) {
	dir := withTempLibrary(t)
	writeLibFile(t, filepath.Join(dir, "音色.sf2"), "RIFF....sfbk")
	writeLibFile(t, filepath.Join(config.LibraryUserDir, "ok.md"), "fine")

	for _, name := range []string{
		"音色.sf2",
		"soundfonts/音色.sf2",
		"user/../../逃逸.md",
		"",
		"不存在.md",
	} {
		if _, err := ReadLibraryFile(name); err == nil {
			t.Errorf("ReadLibraryFile(%q) 应返回错误", name)
		}
	}
	if _, err := ReadLibraryFile("user/ok.md"); err != nil {
		t.Errorf("user/ok.md 应可读取: %v", err)
	}
}
