package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"melodymate/internal/config"
)

func TestMMToolsRegisteredAndGated(t *testing.T) {
	tools := GetMCPTools("proj")
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Function.Name] = true
	}
	for _, want := range []string{"mm_create_plugin", "mm_write_file", "mm_build_plugin", "mm_install_plugin"} {
		if !names[want] {
			t.Fatalf("缺少工具 %s", want)
		}
	}
	// 无工程上下文时 MM 工具不出现（与 arrange 工具同规）
	base := GetMCPTools("")
	for _, tl := range base {
		if isMMTool(tl.Function.Name) {
			t.Fatalf("无工程时不应暴露 %s", tl.Function.Name)
		}
	}
}

func TestMMToolValidation(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"mm_create_plugin", map[string]any{"id": "../evil", "name": "x", "kind": "effect"}, "id 非法"},
		{"mm_create_plugin", map[string]any{"id": "com.a.b", "name": "x", "kind": "nope"}, "kind 必须是"},
		{"mm_write_file", map[string]any{"id": "com.a.b", "path": "../../x.cpp", "content": "x"}, "路径非法"},
		{"mm_write_file", map[string]any{"id": "com.a.b", "path": "C:/x.cpp", "content": "x"}, "路径非法"},
		{"mm_write_file", map[string]any{"id": "com.a.b", "path": "", "content": "x"}, "缺少 path"},
		{"mm_build_plugin", map[string]any{"id": "com.a.b", "confirm": false}, "需要用户确认"},
		{"mm_install_plugin", map[string]any{"id": "com.a.b"}, "先 mm_build_plugin"},
	}
	for _, tc := range cases {
		out, err := executeMMTool(tc.name, tc.args)
		if err != nil {
			t.Fatalf("%s 不应返回 error: %v", tc.name, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf("%s(%v) = %q，期望包含 %q", tc.name, tc.args, out, tc.want)
		}
	}
}

func TestMMWriteFileStaysInsideProject(t *testing.T) {
	dir, err := mmSourceDir("com.test.mmtool")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	out, err := executeMMTool("mm_write_file", map[string]any{
		"id": "com.test.mmtool", "path": "src/plugin.cpp", "content": "// test",
	})
	if err != nil || !strings.Contains(out, "已写入") {
		t.Fatalf("写入失败: %q %v", out, err)
	}
	written := filepath.Join(dir, "src", "plugin.cpp")
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("文件未落盘: %v", err)
	}
	inside, _ := filepath.Rel(config.LibraryDir, written)
	if strings.HasPrefix(inside, "..") {
		t.Fatalf("写到了 Library 之外: %s", written)
	}
}
