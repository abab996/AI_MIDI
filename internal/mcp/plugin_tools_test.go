package mcp

import (
	"strings"
	"testing"

	"melodymate/internal/config"
)

// TestPluginToolsRegistered 三个 P5/P6 工具必须在有工程上下文时注册
// （计划 §3.2.6 的「插件」与「MM 开发」两组）。
func TestPluginToolsRegistered(t *testing.T) {
	tools := GetMCPTools("proj-1")
	want := map[string]bool{
		"plugin_list_params": false,
		"plugin_set_param":   false,
		"mm_list":            false,
	}
	for _, tool := range tools {
		if _, ok := want[tool.Function.Name]; ok {
			want[tool.Function.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("工具未注册：%s", name)
		}
	}
}

func TestPluginRefFromArgs(t *testing.T) {
	env := ToolEnv{ProjectID: "proj-1"}

	// 主总线必须给 slot：主总线就是一条 FX 链，没有"乐器"这回事
	if _, _, err := pluginRefFromArgs(env, map[string]any{"track": "master"}); err == nil {
		t.Fatalf("主总线缺 slot 应当报错")
	}
	track, slot, err := pluginRefFromArgs(env, map[string]any{"track": "master", "slot": float64(3)})
	if err != nil || track != -1 || slot != 3 {
		t.Fatalf("主总线解析 = (%d,%d,%v)，期望 (-1,3,nil)", track, slot, err)
	}
	// 槽位越界
	if _, _, err := pluginRefFromArgs(env, map[string]any{"track": "master", "slot": float64(8)}); err == nil {
		t.Fatalf("槽位 8 应当报错")
	}
	// 不存在的轨道序号必须报可解释错误，而不是静默落到轨道 0
	if _, _, err := pluginRefFromArgs(ToolEnv{ProjectID: "proj-1"}, map[string]any{"track": "99"}); err == nil {
		t.Fatalf("轨道 99 不应解析成功")
	}
}

func TestPluginWhereText(t *testing.T) {
	if got := pluginWhereText(-1, 2); got != "主总线 FX 槽 2" {
		t.Fatalf("主总线描述 = %q", got)
	}
	if got := pluginWhereText(0, -1); got != "轨道 #1（乐器）" {
		t.Fatalf("乐器描述 = %q", got)
	}
	if got := pluginWhereText(4, 0); got != "轨道 #5 FX 槽 0" {
		t.Fatalf("轨 FX 描述 = %q", got)
	}
}

func TestMmListTextEmptyState(t *testing.T) {
	orig := config.LibraryDir
	config.LibraryDir = t.TempDir()
	t.Cleanup(func() { config.LibraryDir = orig })

	got := mmListText()
	if !strings.Contains(got, "还没有安装 MM 插件") {
		t.Fatalf("空态文案不符：%q", got)
	}
}

// TestEveryArrangeToolHasDefinition 防止"分发里有分支、定义被删掉"这类回归：
// 工具名在 isArrangeTool 里返回 true，就必须在 arrangeTools 里真的有定义
// （曾经把 load_plugin 的定义整块替换掉，工具对模型消失、只有调用分支残留）。
func TestEveryArrangeToolHasDefinition(t *testing.T) {
	defined := map[string]bool{}
	for _, tool := range arrangeTools {
		defined[tool.Function.Name] = true
	}
	for name := range map[string]bool{
		"list_sound_sources": true, "list_rack": true, "add_rack_item": true,
		"remove_rack_item": true, "arrange_track": true, "set_track_instrument": true,
		"set_track_mix": true, "mixer_get": true, "set_fx_slot": true,
		"set_master_gain": true, "list_plugins": true, "load_plugin": true,
		"plugin_list_params": true, "plugin_set_param": true, "mm_list": true,
	} {
		if !isArrangeTool(name) {
			continue // 未来若移出分组，这里不该拦
		}
		if !defined[name] {
			t.Fatalf("工具 %s 有调用分支但没有对模型的定义", name)
		}
	}
}

// TestLoadPluginSupportsMM load_plugin 必须声明 MM 装载所需的 format/path，
// 否则 AI 能创建/编译/安装 MM 插件却装不进引擎（闭环断在最后一步）。
func TestLoadPluginSupportsMM(t *testing.T) {
	for _, tool := range arrangeTools {
		if tool.Function.Name != "load_plugin" {
			continue
		}
		props, _ := tool.Function.Parameters["properties"].(map[string]any)
		for _, key := range []string{"id", "format", "path", "track", "slot"} {
			if _, ok := props[key]; !ok {
				t.Fatalf("load_plugin 缺少参数 %s", key)
			}
		}
		if !strings.Contains(tool.Function.Description, "mm") {
			t.Fatalf("load_plugin 描述未说明 MM 装载方式")
		}
		return
	}
	t.Fatalf("load_plugin 工具未定义")
}

func TestArrOfAndNumOf(t *testing.T) {
	if arrOf(nil) != nil || arrOf("x") != nil {
		t.Fatalf("非数组应当返回 nil")
	}
	if got := arrOf([]any{1, 2}); len(got) != 2 {
		t.Fatalf("数组长度 = %d", len(got))
	}
	m := map[string]any{"value": 0.5, "name": "x"}
	if numOf(m, "value") != 0.5 || numOf(m, "missing") != 0 {
		t.Fatalf("numOf 取值不符")
	}
}
