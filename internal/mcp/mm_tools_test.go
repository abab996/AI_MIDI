package mcp

import (
	"os"
	"path/filepath"
	"sort"
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

	// 2) 有一次批准但参数摘要不匹配（内容不同）：拒绝
	store := NewApprovalStore()
	env := ToolEnv{Approval: store}
	if _, err := issueApproval(env, "appr-1", ApprovalRequest{
		Tool:       "mm_build_plugin",
		ArgsDigest: ArgsDigest("mm_build_plugin", map[string]any{"id": id, "content": "different-content"}),
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
	content, cerr := mmProjectContentDigest(dir)
	if cerr != nil {
		t.Fatalf("内容摘要失败: %v", cerr)
	}
	if _, err := issueApproval(env, "appr-2", ApprovalRequest{
		Tool:       "mm_build_plugin",
		ArgsDigest: ArgsDigest("mm_build_plugin", map[string]any{"id": id, "content": content}),
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

// TestApprovalTokenBindsContent 批准令牌绑定工程内容（TOCTOU 防护）：
// 批准后 mm_write_file 改工程 → 令牌整体作废；批准后外部改文件 → 摘要
// 不匹配拒绝；内容未变 → 正常消费。
func TestApprovalTokenBindsContent(t *testing.T) {
	const id = "com.test.mmbind"
	dir, err := mmSourceDir(id)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "src", "plugin.cpp"), []byte("// v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	issue := func(store *ApprovalStore, env ToolEnv, approvalID string) {
		t.Helper()
		content, cerr := mmProjectContentDigest(dir)
		if cerr != nil {
			t.Fatalf("内容摘要失败: %v", cerr)
		}
		if _, err := issueApproval(env, approvalID, ApprovalRequest{
			Tool:       "mm_build_plugin",
			ArgsDigest: ArgsDigest("mm_build_plugin", map[string]any{"id": id, "content": content}),
		}); err != nil {
			t.Fatalf("签发批准失败: %v", err)
		}
		if !store.Approve(approvalID) {
			t.Fatalf("批准应成功")
		}
	}

	// 1) 批准 → mm_write_file 改工程 → 在途批准整体作废（消费报"无效"）
	store := NewApprovalStore()
	env := ToolEnv{ProjectID: "proj-bind", Approval: store}
	issue(store, env, "appr-b1")
	out, _ := executeMMTool(env, "mm_write_file", map[string]any{"id": id, "path": "src/plugin.cpp", "content": "// v2"})
	if !strings.Contains(out, "在途批准已作废") {
		t.Fatalf("mm_write_file 后应提示批准作废: %q", out)
	}
	out, _ = executeMMTool(env, "mm_build_plugin", map[string]any{"id": id, "approval_id": "appr-b1"})
	if !strings.Contains(out, "无效或已被拒绝") {
		t.Fatalf("作废后的令牌应被拒绝: %q", out)
	}

	// 2) 批准 → 外部改文件（绕过 mm_write_file，不触发作废）→ 摘要不匹配拒绝
	store = NewApprovalStore()
	env = ToolEnv{ProjectID: "proj-bind", Approval: store}
	issue(store, env, "appr-b2")
	if err := os.WriteFile(filepath.Join(dir, "src", "plugin.cpp"), []byte("// externally changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ = executeMMTool(env, "mm_build_plugin", map[string]any{"id": id, "approval_id": "appr-b2"})
	if !strings.Contains(out, "不一致") {
		t.Fatalf("内容被外部改动后应拒绝: %q", out)
	}

	// 3) 内容未变 → 正常越过授权闸门（编译本身可失败，但不得再报授权问题）
	store = NewApprovalStore()
	env = ToolEnv{ProjectID: "proj-bind", Approval: store}
	issue(store, env, "appr-b3")
	out, _ = executeMMTool(env, "mm_build_plugin", map[string]any{"id": id, "approval_id": "appr-b3"})
	if strings.Contains(out, "需要用户确认") {
		t.Fatalf("内容未变不应被拒: %q", out)
	}
}

// TestInstallApprovalBindsMMFile 安装令牌绑定签发时确定的 .mm 文件名：
// 签发后新增字典序更靠后的 .mm、或替换已批准包的内容，都被摘要拒绝。
func TestInstallApprovalBindsMMFile(t *testing.T) {
	const id = "com.test.mminstall"
	dir, err := mmSourceDir(id)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.mm"), []byte("pkg-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.mm"), []byte("pkg-c"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewApprovalStore()
	env := ToolEnv{ProjectID: "proj-install", Approval: store}
	content, cerr := mmProjectContentDigest(dir)
	if cerr != nil {
		t.Fatalf("内容摘要失败: %v", cerr)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.mm"))
	sort.Strings(matches)
	if _, err := issueApproval(env, "appr-i1", ApprovalRequest{
		Tool:       "mm_install_plugin",
		ArgsDigest: ArgsDigest("mm_install_plugin", map[string]any{"id": id, "grant": "", "content": content, "mmFile": filepath.Base(matches[len(matches)-1])}),
	}); err != nil {
		t.Fatalf("签发批准失败: %v", err)
	}
	if !store.Approve("appr-i1") {
		t.Fatalf("批准应成功")
	}

	// 1) 签发后新增 d.mm（字典序更靠后）→ 安装目标变了 → 拒绝
	if err := os.WriteFile(filepath.Join(dir, "d.mm"), []byte("pkg-d"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := executeMMTool(env, "mm_install_plugin", map[string]any{"id": id, "approval_id": "appr-i1"})
	if !strings.Contains(out, "不一致") {
		t.Fatalf("签发后新增 .mm 应被拒绝: %q", out)
	}

	// 2) 删掉 d.mm、替换 c.mm 内容 → 内容摘要不匹配 → 拒绝
	if err := os.Remove(filepath.Join(dir, "d.mm")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.mm"), []byte("pkg-c-tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ = executeMMTool(env, "mm_install_plugin", map[string]any{"id": id, "approval_id": "appr-i1"})
	if !strings.Contains(out, "不一致") {
		t.Fatalf("替换已批准包内容应被拒绝: %q", out)
	}

	// 3) 还原内容 → 越过授权闸门（mm.exe 缺失导致安装失败，但不是授权问题）
	if err := os.WriteFile(filepath.Join(dir, "c.mm"), []byte("pkg-c"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ = executeMMTool(env, "mm_install_plugin", map[string]any{"id": id, "approval_id": "appr-i1"})
	if strings.Contains(out, "需要用户确认") {
		t.Fatalf("内容一致不应被授权闸门拒绝: %q", out)
	}
}

func writeManifest(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"com.test","name":"t","kind":"effect"}`), 0o644); err != nil {
		t.Fatalf("写 manifest 失败: %v", err)
	}
}
