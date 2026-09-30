package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		/* 工程不存在先于授权判定被拒；"无批准即拒绝编译"由
		   TestMMBuildRequiresUserApproval 覆盖（那里有真实工程目录） */
		{"mm_build_plugin", map[string]any{"id": "com.a.b", "confirm": true}, "工程不存在"},
		{"mm_install_plugin", map[string]any{"id": "com.a.b"}, "先 mm_build_plugin"},
	}
	for _, tc := range cases {
		out, err := executeMMTool(ToolEnv{}, tc.name, tc.args)
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
	out, err := executeMMTool(ToolEnv{}, "mm_write_file", map[string]any{
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

// TestMMBuildRequiresUserApproval 编译授权必须来自用户确认，而不是模型
// 参数：没有批准令牌 → 拒绝且不启动编译；伪造/过期/换参数 → 同样拒绝。
func TestMMBuildRequiresUserApproval(t *testing.T) {
	const id = "com.test.mmapproval"
	dir, err := mmSourceDir(id)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir)

	// 1) 没有批准通道：拒绝，且不产生任何编译
	out, _ := executeMMTool(ToolEnv{}, "mm_build_plugin", map[string]any{"id": id, "approval_id": "whatever"})
	if !strings.Contains(out, "需要用户确认") {
		t.Fatalf("无确认通道时应拒绝编译: %q", out)
	}

	// 2) 有一次批准但参数摘要不匹配（换了工程）：拒绝
	store := NewApprovalStore()
	env := ToolEnv{Approval: store}
	if err := issueApproval(env, "appr-1", ApprovalRequest{
		Tool:       "mm_build_plugin",
		ArgsDigest: ArgsDigest("mm_build_plugin", map[string]any{"id": "com.other.plugin"}),
	}); err != nil {
		t.Fatalf("签发批准失败: %v", err)
	}
	if !store.Approve("appr-1") {
		t.Fatalf("批准应成功")
	}
	out, _ = executeMMTool(env, "mm_build_plugin", map[string]any{"id": id, "approval_id": "appr-1"})
	if !strings.Contains(out, "不一致") {
		t.Fatalf("参数摘要不匹配应被拒绝: %q", out)
	}

	// 3) 未批准（只签发未点允许）：拒绝
	if err := issueApproval(env, "appr-2", ApprovalRequest{
		Tool:       "mm_build_plugin",
		ArgsDigest: ArgsDigest("mm_build_plugin", map[string]any{"id": id}),
	}); err != nil {
		t.Fatalf("签发批准失败: %v", err)
	}
	out, _ = executeMMTool(env, "mm_build_plugin", map[string]any{"id": id, "approval_id": "appr-2"})
	if !strings.Contains(out, "尚未确认") {
		t.Fatalf("未点击允许时应拒绝: %q", out)
	}

	// 4) 已批准 + 参数一致：越过闸门（编译本身可能失败，但不应再报授权问题）
	if !store.Approve("appr-2") {
		t.Fatalf("批准应成功")
	}
	out, _ = executeMMTool(env, "mm_build_plugin", map[string]any{"id": id, "approval_id": "appr-2"})
	if strings.Contains(out, "需要用户确认") {
		t.Fatalf("已获用户批准仍被拒: %q", out)
	}

	// 5) 一次性：同一令牌不能复用
	out, _ = executeMMTool(env, "mm_build_plugin", map[string]any{"id": id, "approval_id": "appr-2"})
	if !strings.Contains(out, "已被使用") {
		t.Fatalf("批准令牌应一次性: %q", out)
	}
}

// TestApprovalStoreRejectsExpiredAndRejected 过期与拒绝语义
func TestApprovalStoreRejectsExpiredAndRejected(t *testing.T) {
	store := NewApprovalStore()
	store.Issue("appr-exp", ApprovalRequest{Tool: "mm_build_plugin", ArgsDigest: "d"})
	if !store.Approve("appr-exp") {
		t.Fatalf("批准应成功")
	}
	// 把时钟推到 TTL 之后
	store.now = func() time.Time { return time.Now().Add(ApprovalTTL + time.Minute) }
	if err := store.Consume("appr-exp", "mm_build_plugin", "d"); err == nil {
		t.Fatalf("过期令牌应被拒绝")
	}

	store2 := NewApprovalStore()
	store2.Issue("appr-rej", ApprovalRequest{Tool: "mm_build_plugin", ArgsDigest: "d"})
	store2.Approve("appr-rej")
	store2.Reject("appr-rej")
	if err := store2.Consume("appr-rej", "mm_build_plugin", "d"); err == nil {
		t.Fatalf("被拒绝的令牌应无效")
	}
}

// TestArgsDigestIgnoresApprovalID 摘要必须忽略 approval_id 本身，
// 否则"批准后带 id 重试"必然摘要不同，令牌永远无法使用。
func TestArgsDigestIgnoresApprovalID(t *testing.T) {
	a := ArgsDigest("mm_build_plugin", map[string]any{"id": "x", "approval_id": "1"})
	b := ArgsDigest("mm_build_plugin", map[string]any{"id": "x", "approval_id": "2"})
	if a != b {
		t.Fatalf("approval_id 不应影响摘要: %s vs %s", a, b)
	}
	c := ArgsDigest("mm_build_plugin", map[string]any{"id": "y", "approval_id": "1"})
	if a == c {
		t.Fatalf("不同工程必须得到不同摘要")
	}
}

func writeManifest(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"com.test","name":"t","kind":"effect"}`), 0o644); err != nil {
		t.Fatalf("写 manifest 失败: %v", err)
	}
}
