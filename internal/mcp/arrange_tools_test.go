package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"melodymate/internal/config"
	"melodymate/internal/project"
	"melodymate/internal/soundlib"
)

// withArrangeEnv 换绑数据根/项目目录/注册表并建一个项目，返回项目 id
func withArrangeEnv(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldLib, oldSettings, oldProjects := config.ProjectRoot, config.LibraryDir, config.SettingsFile, config.ProjectsDir
	config.ProjectRoot = root
	config.LibraryDir = filepath.Join(root, "Library")
	config.SettingsFile = filepath.Join(root, "settings.json")
	config.ProjectsDir = filepath.Join(root, "projects")
	project.ResetProjectsIndexCache()
	t.Cleanup(func() {
		config.ProjectRoot, config.LibraryDir = oldRoot, oldLib
		config.SettingsFile, config.ProjectsDir = oldSettings, oldProjects
		project.ResetProjectsIndexCache()
	})

	meta, err := project.CreateProject("工具测试项目")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return root, meta.ID
}

func registerSF2(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "Library", "soundfonts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, name+".sf2")
	if err := os.WriteFile(abs, []byte("sf2"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _, err := soundlib.EnsureSF2Source(abs, name)
	if err != nil {
		t.Fatalf("EnsureSF2Source: %v", err)
	}
	return src.ID
}

func toolEnvFor(pid string, events *[]map[string]any) ToolEnv {
	return ToolEnv{
		ProjectID: pid,
		Emit: func(ev map[string]any) {
			if events != nil {
				*events = append(*events, ev)
			}
		},
	}
}

func runTool(t *testing.T, env ToolEnv, name string, args map[string]any) string {
	t.Helper()
	out, err := ExecuteToolEnv(env, name, args)
	if err != nil {
		t.Fatalf("%s 执行报错: %v", name, err)
	}
	return out
}

func TestArrangeToolsRegisteredWithProject(t *testing.T) {
	names := map[string]bool{}
	for _, def := range GetMCPTools("proj-1") {
		names[def.Function.Name] = true
	}
	for _, want := range []string{
		"list_sound_sources", "list_rack", "add_rack_item", "remove_rack_item",
		"arrange_track", "set_track_instrument", "set_track_mix",
	} {
		if !names[want] {
			t.Fatalf("缺少工具 %s（已注册 %v）", want, names)
		}
	}

	// 无项目上下文（stdio MCP 外部客户端）不暴露编曲工具
	noProject := map[string]bool{}
	for _, def := range GetMCPTools("") {
		noProject[def.Function.Name] = true
	}
	if noProject["set_track_instrument"] {
		t.Fatal("无项目 id 时不应暴露编曲工具")
	}
	if !noProject["create_midi"] {
		t.Fatal("文件类工具应始终可用")
	}
}

func TestArrangeToolsNoProjectGivesExplainableError(t *testing.T) {
	out := runTool(t, ToolEnv{}, "list_rack", nil)
	if !strings.Contains(out, "没有打开的项目") {
		t.Fatalf("无项目时的提示不可解释: %q", out)
	}
}

func TestArrangeToolFullLoop(t *testing.T) {
	root, pid := withArrangeEnv(t)
	sf2ID := registerSF2(t, root, "GeneralUser GS")
	var events []map[string]any
	env := toolEnvFor(pid, &events)

	// 1) 查音源
	out := runTool(t, env, "list_sound_sources", map[string]any{"kind": "sf2"})
	if !strings.Contains(out, sf2ID) || !strings.Contains(out, "GeneralUser GS") {
		t.Fatalf("list_sound_sources 输出不含音源: %q", out)
	}

	// 2) 入架（AI 常把 sourceId 传成 itemId：走 set_track_instrument 直传 sourceId 也要成）
	out = runTool(t, env, "add_rack_item", map[string]any{"sourceId": sf2ID, "presetId": "0:48"})
	if !strings.Contains(out, "机架") {
		t.Fatalf("add_rack_item 输出异常: %q", out)
	}
	out = runTool(t, env, "list_rack", nil)
	itemID := extractID(out, "rk_")
	if itemID == "" {
		t.Fatalf("list_rack 未列出条目 id: %q", out)
	}
	if !strings.Contains(out, "未使用") {
		t.Fatalf("新入架条目应显示未使用: %q", out)
	}

	// 3) 指派音源（直接把 sourceId 当 itemId 传：服务端自动入架 + 使用）
	out = runTool(t, env, "set_track_instrument", map[string]any{
		"track": "0", "itemId": itemID, "presetId": "0:48",
	})
	if !strings.Contains(out, "已执行") || !strings.Contains(out, "音源") {
		t.Fatalf("set_track_instrument 输出异常: %q", out)
	}
	out = runTool(t, env, "list_rack", nil)
	if !strings.Contains(out, "#1") {
		t.Fatalf("机架应显示占用轨道: %q", out)
	}

	// 4) 轨道操作
	out = runTool(t, env, "arrange_track", map[string]any{"op": "add", "name": "贝斯"})
	if !strings.Contains(out, "贝斯") {
		t.Fatalf("add 轨道后列表应含新轨: %q", out)
	}
	out = runTool(t, env, "arrange_track", map[string]any{"op": "rename", "track": "贝斯", "name": "Bass"})
	if !strings.Contains(out, "Bass") {
		t.Fatalf("rename 未生效: %q", out)
	}
	out = runTool(t, env, "arrange_track", map[string]any{"op": "list"})
	if !strings.Contains(out, "bpm=") {
		t.Fatalf("list 轨道输出异常: %q", out)
	}

	// 5) 混音
	out = runTool(t, env, "set_track_mix", map[string]any{"track": "0", "gain": 0.5, "pan": -0.3, "mute": true})
	if !strings.Contains(out, "混音") {
		t.Fatalf("set_track_mix 输出异常: %q", out)
	}

	// 6) 钢琴窗音源：track 传 "pianoroll"
	out = runTool(t, env, "set_track_instrument", map[string]any{"track": "pianoroll", "itemId": itemID})
	if !strings.Contains(out, "已执行") {
		t.Fatalf("钢琴窗音源设置失败: %q", out)
	}
	arr := project.LoadArrangementV2(pid)
	pr, _ := arr["pianoRoll"].(map[string]any)
	if pr == nil || pr["itemId"] != itemID {
		t.Fatalf("pianoRoll 未落盘: %v", arr["pianoRoll"])
	}

	// 7) 删条目：被占用时拒绝
	out = runTool(t, env, "remove_rack_item", map[string]any{"itemId": itemID})
	if !strings.Contains(out, "错误") || !strings.Contains(out, "仍被") {
		t.Fatalf("被占用条目应拒绝删除并说明原因: %q", out)
	}

	// 每次写操作都应推 arrangement 事件（前端据此热应用）
	if len(events) == 0 {
		t.Fatal("写操作未推送 arrangement 事件")
	}
	for _, ev := range events {
		if ev["type"] != "arrangement" {
			t.Fatalf("事件类型异常: %v", ev)
		}
		if ev["project"] != pid {
			t.Fatalf("事件缺少项目 id: %v", ev)
		}
		if _, ok := ev["arrangement"]; ok {
			t.Fatal("事件不应携带完整编排数据（前端自己拉取）")
		}
	}

	// 只读工具不应发事件
	before := len(events)
	runTool(t, env, "list_rack", nil)
	runTool(t, env, "arrange_track", map[string]any{"op": "list"})
	if len(events) != before {
		t.Fatalf("只读工具不应推送事件（%d → %d）", before, len(events))
	}
}

func TestArrangeToolAutoAdoptSourceID(t *testing.T) {
	_, pid := withArrangeEnv(t)
	env := toolEnvFor(pid, nil)

	// 未入架就把 sourceId（内置合成器）当 itemId 传：自动入架
	out := runTool(t, env, "set_track_instrument", map[string]any{
		"track": "0", "itemId": "builtin:synth", "presetId": "square",
	})
	if !strings.Contains(out, "自动加入机架") {
		t.Fatalf("应提示自动入架: %q", out)
	}
	arr := project.LoadArrangementV2(pid)
	rack, _ := arr["rack"].(map[string]any)
	items, _ := rack["items"].([]any)
	found := false
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["sourceId"] == "builtin:synth" && m["presetId"] == "square" {
			found = true
		}
	}
	if !found {
		t.Fatalf("自动入架未落盘: %v", items)
	}
}

func TestArrangeToolValidationMessages(t *testing.T) {
	_, pid := withArrangeEnv(t)
	env := toolEnvFor(pid, nil)

	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"set_track_instrument", map[string]any{"track": "0"}, "缺少 itemId"},
		{"set_track_instrument", map[string]any{"itemId": "builtin:synth"}, "缺少 track"},
		{"set_track_instrument", map[string]any{"track": "9", "itemId": "builtin:synth"}, "超出范围"},
		{"add_rack_item", map[string]any{}, "缺少 sourceId"},
		{"add_rack_item", map[string]any{"sourceId": "nope"}, "音源不存在"},
		{"remove_rack_item", map[string]any{}, "缺少 itemId"},
		{"arrange_track", map[string]any{"op": "nuke"}, "只支持"},
		{"arrange_track", map[string]any{"op": "rename", "track": "0"}, "需要 name"},
		{"set_track_mix", map[string]any{"track": "0"}, "至少要给出"},
	}
	for _, tc := range cases {
		out := runTool(t, env, tc.tool, tc.args)
		if !strings.HasPrefix(out, "错误：") {
			t.Fatalf("%s(%v) 应返回可解释错误，实际 %q", tc.tool, tc.args, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf("%s(%v) 错误信息 = %q，期望包含 %q", tc.tool, tc.args, out, tc.want)
		}
	}
}

// extractID 从工具文本里取第一个指定前缀的 id
func extractID(text, prefix string) string {
	idx := strings.Index(text, prefix)
	if idx < 0 {
		return ""
	}
	rest := text[idx:]
	end := 0
	for end < len(rest) {
		c := rest[end]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == ':' {
			end++
			continue
		}
		break
	}
	return rest[:end]
}
