package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aimidi/internal/config"
)

// 用户自定义知识文件必须出现在 system prompt 清单中，且有独立分组说明
// 「文件名即内容概括、主题相关时优先读取」——AI 只有从清单里看到
// user/ 前缀文件才会知道可以读取它们
func TestBuildSystemPromptIncludesUserLibraryFiles(t *testing.T) {
	dir := t.TempDir()
	oldLib, oldUser := config.LibraryDir, config.LibraryUserDir
	config.LibraryDir = dir
	config.LibraryUserDir = filepath.Join(dir, "user")
	t.Cleanup(func() { config.LibraryDir, config.LibraryUserDir = oldLib, oldUser })

	if err := os.WriteFile(filepath.Join(dir, "01_乐理基础.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.LibraryUserDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.LibraryUserDir, "爵士和声入门.md"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := BuildSystemPrompt(nil, 120)
	for _, frag := range []string{
		"### 用户自定义知识文件",
		"`user/爵士和声入门.md` → 爵士和声入门",
		"文件名即内容概括",
	} {
		if !strings.Contains(prompt, frag) {
			t.Errorf("system prompt 缺少 %q", frag)
		}
	}
	// 内置文件仍按编号列出，编号不受用户文件影响
	if !strings.Contains(prompt, "1. `01_乐理基础.md`") {
		t.Errorf("system prompt 内置清单缺失或编号被用户文件挤乱")
	}
}
