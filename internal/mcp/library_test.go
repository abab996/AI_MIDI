package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

// TestReadLibraryFileUnavailableDir 知识库目录整体缺失（典型：安装包漏装
// Library）时，错误必须明确提示「知识库不可用」并包含实际路径——此前
// 返回裸的「文件不存在 + 空可用列表」，故障无法定位。
func TestReadLibraryFileUnavailableDir(t *testing.T) {
	orig := config.LibraryDir
	config.LibraryDir = filepath.Join(t.TempDir(), "Library") // 不存在
	t.Cleanup(func() { config.LibraryDir = orig })

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
	orig := config.LibraryDir
	dir := t.TempDir()
	config.LibraryDir = dir
	t.Cleanup(func() { config.LibraryDir = orig })

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
