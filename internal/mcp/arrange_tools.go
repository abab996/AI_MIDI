package mcp

// 编曲/机架类 MCP 工具：AI 操作"项目用什么音源、轨道挂什么音色"的入口。
//
// 全部走 internal/project 的编排 ops 引擎（与 HTTP /api/projects/{id}/arrangement/ops
// 同一实现）：服务端在项目级写锁内读-改-写 arrangement.json，改完把
// arrangement SSE 事件回推给前端热应用——AI 不必构造完整工程快照，也不会
// 把用户正在编辑的编排整份覆盖掉。
//
// 工具按功能归组以抑制数量膨胀（见《架构升级实施计划》3.2.6）：
//
//	音源：list_sound_sources
//	机架：list_rack / add_rack_item / remove_rack_item
//	编曲：arrange_track / set_track_instrument / set_track_mix

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"melodymate/internal/engine"
	"melodymate/internal/llm"
	"melodymate/internal/project"
	"melodymate/internal/soundlib"
)

// arrangeTools 编曲与机架工具定义
var arrangeTools = []llm.ToolDefinition{
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "list_sound_sources",
			Description: "列出本机音源注册表里的音源（SF2 音色库 / 采样包 / VST3 / MM 插件）。给轨道指定音源前先用它查询有哪些可用音源及其 id。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind": map[string]any{
						"type":        "string",
						"description": "按种类过滤：sf2=音色库、samplepack=采样包、vst3=VST 插件、mm=MM 插件；省略则全部",
						"enum":        []string{"sf2", "samplepack", "vst3", "mm"},
					},
					"q": map[string]any{
						"type":        "string",
						"description": "关键词过滤（名称/厂商/标签/路径）",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "list_rack",
			Description: "列出当前项目输出机架（乐器箱）里的音源条目：条目 id、种类、名称、预设、被哪些轨道使用。轨道的音源只能从机架里选，先看这里再改。",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "add_rack_item",
			Description: "把一个音源加入当前项目的机架（乐器箱）。已在架则原样返回其条目 id。sourceId 取自 list_sound_sources；也可用内置音源 builtin:synth（内置合成器）或 builtin:gm（随包 GeneralUser GS）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sourceId": map[string]any{
						"type":        "string",
						"description": "注册表音源 id（如 sf2_generaluser_xxxxxx）或内置音源 id（builtin:synth / builtin:gm）",
					},
					"presetId": map[string]any{
						"type":        "string",
						"description": "默认预设：SF2 为 \"bank:program\"（如 0:48 弦乐群），合成器为波形名（sawtooth/square/triangle/sine）",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "机架里显示的名称，省略则自动生成",
					},
				},
				"required": []string{"sourceId"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "remove_rack_item",
			Description: "从机架移除一个音源条目。若仍有轨道在用它会被拒绝并列出占用轨道（避免制造无声轨）；确认要删就传 force=true，那些轨道会自动回落到机架里的第一个音源。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"itemId": map[string]any{
						"type":        "string",
						"description": "机架条目 id（list_rack 可见）",
					},
					"force": map[string]any{
						"type":        "boolean",
						"description": "仍有轨道占用时强制移除（占用轨道回落到首个条目）",
					},
				},
				"required": []string{"itemId"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "arrange_track",
			Description: "查看/新增/重命名/删除编曲轨。op=list 列出全部轨道（含音源与混音状态）；op=add 新增轨道；op=rename 重命名；op=remove 删除。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"op": map[string]any{
						"type":        "string",
						"description": "操作：list / add / rename / remove",
						"enum":        []string{"list", "add", "rename", "remove"},
					},
					"track": map[string]any{
						"type":        "string",
						"description": "轨道定位：序号（从 0 开始，如 \"0\"）、轨道 id（如 tr_xxx）或轨道名。rename/remove 必填",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "轨道名（add 时的新轨名 / rename 的新名称）",
					},
					"index": map[string]any{
						"type":        "integer",
						"description": "add 时插入位置（0 起；省略则追加到末尾）",
					},
					"itemId": map[string]any{
						"type":        "string",
						"description": "add 时的机架条目 id（省略则用机架首个音源）",
					},
					"presetId": map[string]any{
						"type":        "string",
						"description": "add 时的预设（bank:program 或波形名）",
					},
				},
				"required": []string{"op"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "set_track_instrument",
			Description: "给编曲轨（或钢琴窗）指定音源与预设。音源必须在机架里（先用 add_rack_item 入架）。track 传 \"pianoroll\" 表示设置钢琴窗的音色。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"track": map[string]any{
						"type":        "string",
						"description": "轨道序号（0 起）/ 轨道 id / 轨道名；或 \"pianoroll\"（钢琴窗音色）",
					},
					"itemId": map[string]any{
						"type":        "string",
						"description": "机架条目 id（list_rack 可见）",
					},
					"presetId": map[string]any{
						"type":        "string",
						"description": "预设：SF2 为 \"bank:program\"，合成器为波形名；省略则用条目默认预设",
					},
				},
				"required": []string{"track", "itemId"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "set_track_mix",
			Description: "调整轨道混音：音量（gain 0.0~1.0）、声像（pan -1.0 左 ~ 1.0 右）、静音（mute）、独奏（solo）。只传需要改的字段。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"track": map[string]any{
						"type":        "string",
						"description": "轨道序号（0 起）/ 轨道 id / 轨道名",
					},
					"gain": map[string]any{
						"type":        "number",
						"description": "音量 0.0 ~ 1.0（0.8 ≈ 原音量）",
					},
					"pan": map[string]any{
						"type":        "number",
						"description": "声像 -1.0（左）~ 1.0（右），0 为居中",
					},
					"mute": map[string]any{
						"type":        "boolean",
						"description": "是否静音",
					},
					"solo": map[string]any{
						"type":        "boolean",
						"description": "是否独奏",
					},
				},
				"required": []string{"track"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mixer_get",
			Description: "读取混音台状态：每条通道的音量/声像/静音/独奏、FX 槽占用，以及主总线增益。回答\"现在混音台什么状态\"或改混音前先读一次。",
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "set_fx_slot",
			Description: "设置一条通道的 FX 槽参数：旁通/启用（bypass）、干湿比（wet 0~1）、清空（off）。track 传 \"master\" 表示主总线。插件装载在 P5/P6 开放——本工具只调槽参数，不要传 plugin 去装载。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"track": map[string]any{
						"type":        "string",
						"description": "轨道序号（0 起）/ 轨道 id / 轨道名；或 \"master\"（主总线）",
					},
					"slot": map[string]any{
						"type":        "integer",
						"description": "槽位序号 0 ~ 7",
					},
					"bypass": map[string]any{
						"type":        "boolean",
						"description": "true=旁通，false=启用",
					},
					"wet": map[string]any{
						"type":        "number",
						"description": "干湿比 0.0（全干）~ 1.0（全湿）",
					},
					"off": map[string]any{
						"type":        "boolean",
						"description": "true=清空该槽",
					},
				},
				"required": []string{"slot"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "set_master_gain",
			Description: "设置主总线增益（0.0 ~ 4.0，1.0 = 0dB）。整体太吵/太轻时用它，而不是逐轨改音量。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"gain": map[string]any{
						"type":        "number",
						"description": "主总线增益 0.0 ~ 4.0（1.0 = 0dB）",
					},
				},
				"required": []string{"gain"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "list_plugins",
			Description: "列出已扫描到的 VST3 插件（乐器与效果器）。scan=true 时先扫描系统插件目录（首次较慢）。返回的 id 用于 load_plugin。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"scan": map[string]any{
						"type":        "boolean",
						"description": "是否先触发扫描（默认 false，只读已知清单）",
					},
					"onlyInstrument": map[string]any{
						"type":        "boolean",
						"description": "只列乐器插件（给轨道挂音色时用）",
					},
				},
				"additionalProperties": false,
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "load_plugin",
			Description: "把插件装载到轨道（乐器）或 FX 槽（效果器）。track 传 \"master\" 表示主总线 FX 槽。装载乐器会替换该轨原有的 SF2/合成器音色。VST3 用 id（list_plugins 返回）；MM 插件要额外给 format=\"mm\" 与 path（mm_list 返回的 libPath，因为引擎按路径装载原生库）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "插件 id（VST3 用 list_plugins；MM 用 mm_list 的 id）",
					},
					"format": map[string]any{
						"type":        "string",
						"description": "插件格式：省略 = VST3；\"mm\" = MM 插件（需同时给 path）",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "MM 插件的原生库路径（mm_list 的 libPath）；VST3 不需要",
					},
					"track": map[string]any{
						"type":        "string",
						"description": "轨道序号（0 起）/ id / 名称；或 \"master\"（主总线）",
					},
					"slot": map[string]any{
						"type":        "integer",
						"description": "FX 槽序号 0~7；省略 = 作为该轨的乐器装载",
					},
				},
				"required": []string{"id"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "plugin_list_params",
			Description: "列出某个插件实例的参数表（VST3 与 MM 共用）。目标用 track/slot 指定：给 slot = FX 槽上的效果器；不给 slot = 该轨的乐器；track 传 \"master\" 表示主总线 FX 槽。参数 value 为 0~1 归一化值。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"track": map[string]any{
						"type":        "string",
						"description": "轨道序号（0 起）/ id / 名称；或 \"master\"（主总线）",
					},
					"slot": map[string]any{
						"type":        "integer",
						"description": "FX 槽序号 0~7；省略 = 该轨的乐器",
					},
				},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "plugin_set_param",
			Description: "设置插件参数（VST3 与 MM 共用）。param 可传参数名或序号；value 为 0~1 归一化值（0=最小、1=最大，与界面上滑杆一致）。改完返回该参数的新值与文本。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"track": map[string]any{
						"type":        "string",
						"description": "轨道序号（0 起）/ id / 名称；或 \"master\"（主总线）",
					},
					"slot": map[string]any{
						"type":        "integer",
						"description": "FX 槽序号 0~7；省略 = 该轨的乐器",
					},
					"param": map[string]any{
						"type":        "string",
						"description": "参数名（如 \"Cutoff\"）或参数序号（0 起，先 plugin_list_params 查）",
					},
					"value": map[string]any{
						"type":        "number",
						"description": "0~1 归一化目标值",
					},
				},
				"required": []string{"param", "value"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_list",
			Description: "列出本机已安装的 MM 插件（Library/plugins 下，含版本、能力清单、作者是否为 AI、以及装载用的 libPath）。装载用 load_plugin(id=<插件 id>, format=\"mm\", path=<libPath>)：给 slot 就是 FX 槽效果器，不给 slot 就是该轨乐器。",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
}

// isArrangeTool 判断是否为编曲/机架/混音类工具
func isArrangeTool(name string) bool {
	switch name {
	case "list_sound_sources", "list_rack", "add_rack_item", "remove_rack_item",
		"arrange_track", "set_track_instrument", "set_track_mix",
		"mixer_get", "set_fx_slot", "set_master_gain",
		"list_plugins", "load_plugin",
		"plugin_list_params", "plugin_set_param", "mm_list":
		return true
	}
	return false
}

// executeArrangeTool 执行编曲/机架工具；env.ProjectID 为空时报可解释错误
func executeArrangeTool(env ToolEnv, name string, args map[string]any) (string, error) {
	if strings.TrimSpace(env.ProjectID) == "" {
		return "错误：当前没有打开的项目，无法操作编曲与机架。", nil
	}
	if name == "list_sound_sources" {
		return listSoundSourcesText(args), nil
	}

	// list_* 只读：直接读盘，不写文件、不发事件
	if name == "list_rack" {
		return listRackText(env.ProjectID), nil
	}
	if name == "mixer_get" {
		return mixerText(env.ProjectID), nil
	}
	if name == "list_plugins" {
		return listPluginsText(args), nil
	}
	if name == "load_plugin" {
		return loadPluginText(env, args), nil
	}
	if name == "plugin_list_params" {
		return pluginListParamsText(env, args), nil
	}
	if name == "plugin_set_param" {
		return pluginSetParamText(env, args), nil
	}
	if name == "mm_list" {
		return mmListText(), nil
	}
	if name == "arrange_track" && strings.ToLower(strings.TrimSpace(stringArg(args, "op"))) == "list" {
		return listTracksText(env.ProjectID), nil
	}

	op, err := arrangeOpFor(name, args)
	if err != nil {
		return "错误：" + err.Error(), nil
	}
	res, err := project.ApplyOps(env.ProjectID, []map[string]any{op})
	if err != nil {
		return "错误：" + err.Error(), nil
	}
	if env.Emit != nil {
		// 不带完整编排数据：前端收到后自己 GET /arrangement 拉取并热应用
		env.Emit(map[string]any{
			"type":    "arrangement",
			"project": env.ProjectID,
			"rev":     res.Rev,
			"changed": res.Changed,
			"summary": res.Summary,
			"source":  "ai",
			"tool":    name,
		})
	}

	var b strings.Builder
	if len(res.Summary) > 0 {
		b.WriteString("已执行：" + strings.Join(res.Summary, "；") + "\n")
	}
	b.WriteString(afterText(env.ProjectID, name, args))
	return strings.TrimRight(b.String(), "\n"), nil
}

// arrangeOpFor 把工具调用翻译成一条编排 op
func arrangeOpFor(name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "add_rack_item":
		sourceID := stringArg(args, "sourceId")
		if sourceID == "" {
			return nil, fmt.Errorf("缺少 sourceId")
		}
		op := map[string]any{"op": "addRackItem", "sourceId": sourceID}
		if v := stringArg(args, "presetId"); v != "" {
			op["presetId"] = v
		}
		if v := stringArg(args, "name"); v != "" {
			op["name"] = v
		}
		return op, nil

	case "remove_rack_item":
		itemID := stringArg(args, "itemId")
		if itemID == "" {
			return nil, fmt.Errorf("缺少 itemId")
		}
		op := map[string]any{"op": "removeRackItem", "itemId": itemID}
		if v, ok := args["force"]; ok && v != nil {
			op["force"] = v
		}
		return op, nil

	case "arrange_track":
		switch strings.ToLower(strings.TrimSpace(stringArg(args, "op"))) {
		case "add":
			op := map[string]any{"op": "addTrack"}
			for _, key := range []string{"name", "itemId", "presetId"} {
				if v := stringArg(args, key); v != "" {
					op[key] = v
				}
			}
			if v, ok := args["index"]; ok && v != nil {
				op["index"] = v
			}
			return op, nil
		case "rename":
			track, err := requireTrack(args)
			if err != nil {
				return nil, err
			}
			nameText := stringArg(args, "name")
			if nameText == "" {
				return nil, fmt.Errorf("rename 需要 name")
			}
			return map[string]any{"op": "renameTrack", "track": track, "name": nameText}, nil
		case "remove":
			track, err := requireTrack(args)
			if err != nil {
				return nil, err
			}
			return map[string]any{"op": "removeTrack", "track": track}, nil
		default:
			return nil, fmt.Errorf("op 只支持 list / add / rename / remove")
		}

	case "set_track_instrument":
		track, err := requireTrack(args)
		if err != nil {
			return nil, err
		}
		itemID := stringArg(args, "itemId")
		if itemID == "" {
			return nil, fmt.Errorf("缺少 itemId（音源需先用 add_rack_item 入架）")
		}
		if isPianoRollRef(track) {
			op := map[string]any{"op": "setPianoRoll", "itemId": itemID}
			if v := stringArg(args, "presetId"); v != "" {
				op["presetId"] = v
			}
			return op, nil
		}
		op := map[string]any{"op": "setTrackSource", "track": trackRef(track), "itemId": itemID}
		if v := stringArg(args, "presetId"); v != "" {
			op["presetId"] = v
		}
		return op, nil

	case "set_track_mix":
		track, err := requireTrack(args)
		if err != nil {
			return nil, err
		}
		op := map[string]any{"op": "setTrackMix", "track": trackRef(track)}
		for _, key := range []string{"gain", "pan", "mute", "solo"} {
			if v, ok := args[key]; ok && v != nil {
				op[key] = v
			}
		}
		if len(op) == 2 {
			return nil, fmt.Errorf("至少要给出 gain / pan / mute / solo 之一")
		}
		return op, nil

	case "set_fx_slot":
		op := map[string]any{"op": "setFxSlot"}
		track := strings.ToLower(strings.TrimSpace(stringArg(args, "track")))
		if track == "master" || track == "主总线" {
			op["master"] = true
		} else {
			t, err := requireTrack(args)
			if err != nil {
				return nil, err
			}
			op["track"] = trackRef(t)
		}
		for _, key := range []string{"slot", "bypass", "wet", "off"} {
			if v, ok := args[key]; ok && v != nil {
				op[key] = v
			}
		}
		if _, ok := op["slot"]; !ok {
			return nil, fmt.Errorf("缺少 slot（0 ~ 7）")
		}
		if len(op) <= 2 {
			return nil, fmt.Errorf("至少要给出 bypass / wet / off 之一")
		}
		return op, nil

	case "set_master_gain":
		gain, ok := args["gain"]
		if !ok || gain == nil {
			return nil, fmt.Errorf("缺少 gain（0.0 ~ 4.0）")
		}
		return map[string]any{"op": "setMaster", "gain": gain}, nil
	}
	return nil, fmt.Errorf("未知工具 %s", name)
}

/* ═══════════ 只读文本输出 ═══════════ */

func listSoundSourcesText(args map[string]any) string {
	kind := strings.ToLower(strings.TrimSpace(stringArg(args, "kind")))
	list := soundlib.List(kind, stringArg(args, "q"))
	if len(list) == 0 {
		if kind != "" {
			return fmt.Sprintf("音源注册表里没有 %s 类音源。可提示用户在设置页「声音管理」里添加，或上传 .sf2 音色库。", kind)
		}
		return "音源注册表为空。可提示用户在设置页「声音管理」里添加采样包或上传 .sf2 音色库。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "音源注册表（%d 条）：\n", len(list))
	for _, s := range list {
		bits := []string{}
		switch s.Kind {
		case soundlib.KindSF2:
			if s.Runtime.PresetCount > 0 {
				bits = append(bits, fmt.Sprintf("%d 预设", s.Runtime.PresetCount))
			}
			if s.Runtime.Size > 0 {
				bits = append(bits, humanSize(s.Runtime.Size))
			}
		case soundlib.KindSamplePack:
			bits = append(bits, fmt.Sprintf("%d 目录", len(s.Dirs)), fmt.Sprintf("%d 音频文件", s.Runtime.SampleFiles))
		}
		switch s.Origin {
		case soundlib.OriginBuiltin:
			bits = append(bits, "内置")
		case soundlib.OriginAI:
			bits = append(bits, "AI 生成")
		}
		if s.Runtime.Missing {
			bits = append(bits, "⚠ 文件缺失")
		}
		if !s.Enabled {
			bits = append(bits, "已停用")
		}
		fmt.Fprintf(&b, "- %s [%s] %s", s.ID, strings.ToUpper(s.Kind), s.Name)
		if s.Vendor != "" {
			fmt.Fprintf(&b, "（%s）", s.Vendor)
		}
		if len(bits) > 0 {
			fmt.Fprintf(&b, "  %s", strings.Join(bits, " · "))
		}
		b.WriteString("\n")
	}
	b.WriteString("内置音源（不在注册表，可直接 add_rack_item）：builtin:synth（内置合成器，预设=波形名）、builtin:gm（随包 GeneralUser GS，预设=bank:program）")
	return b.String()
}

func listRackText(projectID string) string {
	arr := loadArrangement(projectID)
	items := arrangementRackItems(arr)
	if len(items) == 0 {
		return "机架（乐器箱）是空的。先用 list_sound_sources 查询音源，再 add_rack_item 入架。"
	}
	usage := map[string][]string{}
	for i, t := range arrangementTracks(arr) {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		src, _ := tm["source"].(map[string]any)
		itemID := mapString(src, "itemId")
		if itemID == "" {
			continue
		}
		name := mapString(tm, "name")
		usage[itemID] = append(usage[itemID], fmt.Sprintf("#%d %s", i+1, name))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "机架（%d 个音源）：\n", len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		id := mapString(m, "id")
		fmt.Fprintf(&b, "- %s [%s] %s", id, strings.ToUpper(mapString(m, "kind")), mapString(m, "name"))
		if p := mapString(m, "presetId"); p != "" {
			fmt.Fprintf(&b, "  预设 %s", p)
		}
		if used := usage[id]; len(used) > 0 {
			fmt.Fprintf(&b, "  ← %s 在用", strings.Join(used, "、"))
		} else {
			b.WriteString("  （未使用）")
		}
		b.WriteString("\n")
	}
	b.WriteString("音源只能在机架范围内指派：set_track_instrument 的 itemId 必须取自上面这些 id。")
	return b.String()
}

func listTracksText(projectID string) string {
	arr := loadArrangement(projectID)
	tracks := arrangementTracks(arr)
	if len(tracks) == 0 {
		return "当前工程还没有轨道。可用 arrange_track(op=add) 新建。"
	}
	itemNames := map[string]string{}
	for _, it := range arrangementRackItems(arr) {
		if m, ok := it.(map[string]any); ok {
			itemNames[mapString(m, "id")] = mapString(m, "name")
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "编曲轨（%d 条，bpm=%v）：\n", len(tracks), arr["bpm"])
	for i, t := range tracks {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		src, _ := tm["source"].(map[string]any)
		itemID := mapString(src, "itemId")
		label := itemNames[itemID]
		if label == "" {
			label = "⚠ 音源未在机架中"
		}
		fmt.Fprintf(&b, "- #%d %s  id=%s  音源=%s", i, mapString(tm, "name"), mapString(tm, "id"), label)
		if p := mapString(src, "presetId"); p != "" {
			fmt.Fprintf(&b, "（预设 %s）", p)
		}
		flags := []string{}
		if mapBool(tm, "mute") {
			flags = append(flags, "静音")
		}
		if mapBool(tm, "solo") {
			flags = append(flags, "独奏")
		}
		fmt.Fprintf(&b, "  音量=%.2f", mapFloat(tm, "volume"))
		if len(flags) > 0 {
			fmt.Fprintf(&b, "  %s", strings.Join(flags, "/"))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// mixerText 混音台只读输出：每通道 + 主总线 + FX 槽占用
func mixerText(projectID string) string {
	arr := loadArrangement(projectID)
	tracks := arrangementTracks(arr)
	mixer, _ := arr["mixer"].(map[string]any)
	channels, _ := mixer["channels"].(map[string]any)
	master, _ := mixer["master"].(map[string]any)

	var b strings.Builder
	fmt.Fprintf(&b, "混音台（%d 条通道）：\n", len(tracks))
	for i, t := range tracks {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		id := mapString(tm, "id")
		var ch map[string]any
		if channels != nil {
			ch, _ = channels[id].(map[string]any)
		}
		gain := mapFloat(tm, "volume")
		pan, mute, solo := float64(0), mapBool(tm, "mute"), mapBool(tm, "solo")
		if ch != nil {
			if v, ok := ch["gain"].(float64); ok && gain == 0 {
				gain = v
			}
			pan = mapFloat(ch, "pan")
			if _, ok := ch["mute"]; ok {
				mute = mapBool(ch, "mute")
			}
			if _, ok := ch["solo"]; ok {
				solo = mapBool(ch, "solo")
			}
		}
		fmt.Fprintf(&b, "- #%d %s  id=%s  音量=%.2f(%.1fdB)  声像=%s", i, mapString(tm, "name"), id,
			gain, dbOf(gain), panText(pan))
		flags := []string{}
		if mute {
			flags = append(flags, "静音")
		}
		if solo {
			flags = append(flags, "独奏")
		}
		if len(flags) > 0 {
			fmt.Fprintf(&b, "  %s", strings.Join(flags, "/"))
		}
		if occupied := fxUsedText(ch); occupied != "" {
			fmt.Fprintf(&b, "  FX=%s", occupied)
		}
		b.WriteString("\n")
	}
	masterGain := 1.0
	if master != nil {
		if v, ok := master["gain"].(float64); ok {
			masterGain = v
		}
	}
	fmt.Fprintf(&b, "主总线：增益=%.2f（%.1fdB）", masterGain, dbOf(masterGain))
	if occupied := fxUsedText(master); occupied != "" {
		fmt.Fprintf(&b, "  FX=%s", occupied)
	}
	b.WriteString("\n（FX 槽只承载旁通/干湿比；插件装载在 P5/P6 开放）")
	return b.String()
}

// fxUsedText 已使用（区别于默认「空且旁通」）的槽位摘要
func fxUsedText(ch map[string]any) string {
	if ch == nil {
		return ""
	}
	fx, _ := ch["fx"].([]any)
	used := []string{}
	for i, e := range fx {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		plugin := mapString(em, "plugin")
		bypass := true
		if v, ok := em["bypass"].(bool); ok {
			bypass = v
		}
		wet := 1.0
		if v, ok := em["wet"].(float64); ok {
			wet = v
		}
		if plugin == "" && bypass && wet == 1 {
			continue
		}
		desc := fmt.Sprintf("%d:%s", i, plugin)
		if plugin == "" {
			desc = fmt.Sprintf("%d(空)", i)
		}
		if bypass {
			desc += "·旁通"
		}
		if wet != 1 {
			desc += fmt.Sprintf("·wet %.0f%%", wet*100)
		}
		used = append(used, desc)
	}
	return strings.Join(used, " ")
}

func panText(pan float64) string {
	if pan == 0 {
		return "居中"
	}
	if pan < 0 {
		return fmt.Sprintf("L%d", int(-pan*100+0.5))
	}
	return fmt.Sprintf("R%d", int(pan*100+0.5))
}

func dbOf(gain float64) float64 {
	if gain <= 0.0001 {
		return -60
	}
	return 20 * math.Log10(gain)
}

// listPluginsText 插件清单文本（引擎未启动时给可解释提示）
func listPluginsText(args map[string]any) string {
	sup := engine.Get()
	if sup == nil {
		return "引擎未启动：VST3 插件需要在桌面版（原生引擎）下使用。可用 list_sound_sources 查看 SF2/采样包音源。"
	}
	var list *engine.PluginList
	var err error
	if mapBool(args, "scan") {
		list, err = sup.ScanPlugins(nil, 0)
		if err == nil && list != nil {
			_ = soundlib.SyncVST3Plugins(list.Plugins)
		}
	} else {
		list, err = sup.ListPlugins()
	}
	if err != nil {
		return "错误：" + err.Error()
	}
	if list.Count == 0 {
		return "已知插件清单为空。用 list_plugins(scan=true) 扫描系统插件目录（" +
			strings.Join(list.Formats, "/") + " 格式已注册）。"
	}
	onlyInstrument := mapBool(args, "onlyInstrument")
	var b strings.Builder
	fmt.Fprintf(&b, "VST3 插件（%d 个）：\n", list.Count)
	for _, p := range list.Plugins {
		if onlyInstrument && !p.Instrument {
			continue
		}
		kind := "效果器"
		if p.Instrument {
			kind = "乐器"
		}
		fmt.Fprintf(&b, "- [%s] %s（%s）id=%s\n", kind, p.Name, p.Vendor, p.ID)
	}
	return b.String()
}

// loadPluginText 装载插件 + 落一份槽位状态（AI 不必再查）
func loadPluginText(env ToolEnv, args map[string]any) string {
	id := stringArg(args, "id")
	if id == "" {
		return "错误：缺少 id（先 list_plugins）"
	}
	registered, err := findAIPluginSource(id, stringArg(args, "path"))
	if err != nil {
		return "错误：" + err.Error()
	}
	resolved, err := soundlib.ResolvePlugin(registered.ID)
	if err != nil {
		return "错误：" + err.Error()
	}
	slot := -1
	if value, ok := args["slot"].(float64); ok {
		if value != math.Trunc(value) || value < 0 || value >= 8 {
			return "错误：slot 超出范围（0-7）"
		}
		slot = int(value)
	}
	master := strings.EqualFold(stringArg(args, "track"), "master") || stringArg(args, "track") == "主总线"
	if master && slot < 0 {
		return "错误：主总线必须指定 FX 槽"
	}
	if slot < 0 && resolved.Category != "instrument" {
		return "错误：该插件不是乐器"
	}
	if slot >= 0 && resolved.Category == "instrument" {
		return "错误：FX 槽不接受乐器"
	}
	op := map[string]any{"op": "setFxSlot", "slot": slot, "plugin": resolved.ID, "sourceId": registered.ID, "bypass": false}
	where := "主总线"
	if master {
		op["master"] = true
	} else {
		track, err := requireTrack(args)
		if err != nil {
			return "错误：" + err.Error()
		}
		index, err := resolveTrackIndex(env.ProjectID, track)
		if err != nil {
			return "错误：" + err.Error()
		}
		tracks := arrangementTracks(loadArrangement(env.ProjectID))
		if index >= len(tracks) {
			return "错误：轨道已删除"
		}
		target, _ := tracks[index].(map[string]any)
		trackID := stringArg(target, "id")
		op["track"] = trackID
		where = "轨道 " + trackID
		if slot < 0 {
			op = map[string]any{"op": "setTrackSource", "track": trackID, "itemId": registered.ID}
		}
	}
	result, err := project.ApplyOps(env.ProjectID, []map[string]any{op})
	if err != nil {
		return "错误：插件引用保存失败：" + err.Error()
	}
	if env.Emit != nil {
		env.Emit(map[string]any{"type": "arrangement", "project": env.ProjectID, "rev": result.Rev, "changed": []string{"mixer", "tracks", "rack"}, "summary": []string{"指派插件 " + registered.ID}, "source": "ai", "tool": "load_plugin"})
	}
	return "已将插件指派到 " + where + "：" + registered.ID + "。当前工程由音频协调器装载并恢复状态。"
}

func findAIPluginSource(id, legacyPath string) (soundlib.View, error) {
	if source, err := soundlib.Get(id); err == nil {
		return source, nil
	}
	var matches []soundlib.View
	for _, source := range soundlib.List("", "") {
		if source.Kind != soundlib.KindMM && source.Kind != soundlib.KindVST3 {
			continue
		}
		if source.ClassUID != id {
			continue
		}
		if legacyPath != "" && source.Kind == soundlib.KindMM {
			ref, err := soundlib.ResolvePlugin(source.ID)
			if err != nil || !strings.EqualFold(filepath.Clean(ref.Path), filepath.Clean(legacyPath)) {
				continue
			}
		}
		matches = append(matches, source)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return soundlib.View{}, fmt.Errorf("多个版本已安装，请使用注册表 sourceId")
	}
	sup := engine.Get()
	if sup == nil {
		return soundlib.View{}, fmt.Errorf("插件未登记：%s", id)
	}
	list, err := sup.ListPlugins()
	if err != nil {
		return soundlib.View{}, err
	}
	for _, plugin := range list.Plugins {
		if plugin.ID != id {
			continue
		}
		source, err := soundlib.Add(soundlib.Source{Kind: soundlib.KindVST3, Name: plugin.Name, Vendor: plugin.Vendor, Path: plugin.Path, ClassUID: plugin.ID, Category: plugin.Category, Enabled: true, Origin: soundlib.OriginUser})
		if err != nil {
			return soundlib.View{}, err
		}
		return soundlib.Get(source.ID)
	}
	return soundlib.View{}, fmt.Errorf("插件未登记：%s", id)
}

// pluginStateText 宿主机内状态摘要
func pluginStateText(sup *engine.Supervisor) string {
	state, err := sup.PluginState()
	if err != nil {
		return ""
	}
	inst, _ := state["instruments"].([]any)
	fx, _ := state["effects"].([]any)
	var b strings.Builder
	fmt.Fprintf(&b, "当前宿主：%d 个乐器轨 / %d 个效果槽\n", len(inst), len(fx))
	for _, it := range inst {
		if m, ok := it.(map[string]any); ok {
			fmt.Fprintf(&b, "- 轨 #%v ← %v\n", m["track"], m["name"])
		}
	}
	for _, it := range fx {
		if m, ok := it.(map[string]any); ok {
			if b2, isBool := m["master"].(bool); isBool && b2 {
				fmt.Fprintf(&b, "- 主总线 FX %v ← %v\n", m["slot"], m["name"])
			} else {
				fmt.Fprintf(&b, "- 轨 #%v FX %v ← %v\n", m["track"], m["slot"], m["name"])
			}
		}
	}
	return b.String()
}

// resolveTrackIndex 把轨道引用（序号/id/名称）解析成引擎槽位序号（0 起）
func resolveTrackIndex(projectID, ref string) (int, error) {
	arr := loadArrangement(projectID)
	tracks := arrangementTracks(arr)
	for i, t := range tracks {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if mapString(tm, "id") == ref || mapString(tm, "name") == ref {
			return i, nil
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(ref)); err == nil {
		if n >= 0 && n < len(tracks) {
			return n, nil
		}
		return 0, fmt.Errorf("轨道序号超出范围（0 ~ %d）", len(tracks)-1)
	}
	return 0, fmt.Errorf("找不到轨道 %q", ref)
}

// pluginRefFromArgs 解析工具参数里的插件目标 {track|master, slot} → 引擎 (track, slot)。
// track = -1 表示主总线；slot = -1 表示该轨乐器。
func pluginRefFromArgs(env ToolEnv, args map[string]any) (int, int, error) {
	slot := -1
	if v, ok := args["slot"]; ok && v != nil {
		if f, isNum := v.(float64); isNum {
			slot = int(f)
		}
	}
	if slot >= 8 || slot < -1 {
		return 0, 0, fmt.Errorf("槽位序号超出范围（0 ~ 7）")
	}
	trackText := strings.ToLower(strings.TrimSpace(stringArg(args, "track")))
	if trackText == "master" || trackText == "主总线" {
		if slot < 0 {
			return 0, 0, fmt.Errorf("主总线是 FX 链，必须给 slot（0~7）")
		}
		return -1, slot, nil
	}
	track, err := requireTrack(args)
	if err != nil {
		return 0, 0, err
	}
	idx, err := resolveTrackIndex(env.ProjectID, track)
	if err != nil {
		return 0, 0, err
	}
	return idx, slot, nil
}

// pluginWhereText 目标的中文描述（回执里用，避免 AI 猜"改的是哪个"）
func pluginWhereText(track, slot int) string {
	where := "轨道 #" + fmt.Sprint(track+1)
	if track < 0 {
		where = "主总线"
	}
	if slot >= 0 {
		return fmt.Sprintf("%s FX 槽 %d", where, slot)
	}
	return where + "（乐器）"
}

// pluginParamsText 列出插件参数表（VST3 与 MM 共用一条路径）
func pluginListParamsText(env ToolEnv, args map[string]any) string {
	sup := engine.Get()
	if sup == nil {
		return "引擎未启动：插件参数需要在桌面版（原生引擎）下读取。"
	}
	track, slot, err := pluginRefFromArgs(env, args)
	if err != nil {
		return "错误：" + err.Error()
	}
	out, err := sup.PluginParams(track, slot)
	if err != nil {
		return "错误：" + err.Error()
	}
	params, _ := out["params"].([]any)
	if len(params) == 0 {
		return pluginWhereText(track, slot) + " 上还没有插件（先用 load_plugin 装载）。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 的插件参数（value 为 0~1 归一化值）：\n", pluginWhereText(track, slot))
	for _, raw := range params {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := mapString(m, "name")
		id := mapString(m, "id")
		label := name
		if label == "" {
			label = id
		}
		line := fmt.Sprintf("  [%v] %s = %.4f", m["index"], label, numOf(m, "value"))
		if t := mapString(m, "text"); t != "" {
			line += "（" + t + "）"
		}
		if id != "" && id != label {
			line += " id=" + id
		}
		// 非归一化参数（MM 给的是实际范围）把范围也带上，避免 AI 误判量纲
		if mn, ok := m["min"].(float64); ok {
			if mx, ok2 := m["max"].(float64); ok2 {
				line += fmt.Sprintf(" 范围 %.3f~%.3f", mn, mx)
			}
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("改值：plugin_set_param(param=\"名称或序号\", value=0~1)")
	return b.String()
}

// pluginSetParamText 写插件参数：param 支持名称（精确/忽略大小写）或序号
func pluginSetParamText(env ToolEnv, args map[string]any) string {
	sup := engine.Get()
	if sup == nil {
		return "引擎未启动：插件参数需要在桌面版（原生引擎）下设置。"
	}
	ref := strings.TrimSpace(stringArg(args, "param"))
	if ref == "" {
		return "错误：缺少 param（参数名或序号，先 plugin_list_params 查）"
	}
	raw, ok := args["value"]
	if !ok || raw == nil {
		return "错误：缺少 value（0~1 归一化值）"
	}
	value, ok := raw.(float64)
	if !ok {
		return "错误：value 必须是数字（0~1）"
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}

	track, slot, err := pluginRefFromArgs(env, args)
	if err != nil {
		return "错误：" + err.Error()
	}
	before, err := sup.PluginParams(track, slot)
	if err != nil {
		return "错误：" + err.Error()
	}
	params, _ := before["params"].([]any)
	if len(params) == 0 {
		return pluginWhereText(track, slot) + " 上还没有插件（先用 load_plugin 装载）。"
	}

	/* 先按名称匹配（忽略大小写），再按序号：AI 通常只知道参数名 */
	index := -1
	want := strings.ToLower(ref)
	for _, p := range params {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if strings.ToLower(mapString(m, "name")) == want || strings.ToLower(mapString(m, "id")) == want {
			if f, isNum := m["index"].(float64); isNum {
				index = int(f)
			}
			break
		}
	}
	if index < 0 {
		if n, aerr := strconv.Atoi(ref); aerr == nil {
			if n >= 0 && n < len(params) {
				index = n
			} else {
				return fmt.Sprintf("错误：参数序号超出范围（0 ~ %d）", len(params)-1)
			}
		}
	}
	if index < 0 {
		return fmt.Sprintf("错误：找不到参数 %q（先 plugin_list_params 取名称/序号）", ref)
	}

	if err := sup.SetPluginParam(track, slot, index, value); err != nil {
		return "错误：" + err.Error()
	}
	/* 回读引擎的权威值：界面与插件可能对步进做量化，回读才知道"实际生效值" */
	after, err := sup.PluginParams(track, slot)
	if err != nil {
		return fmt.Sprintf("已写入 %s 的参数 %q（无法回读校验：%v）", pluginWhereText(track, slot), ref, err)
	}
	for _, p := range arrOf(after["params"]) {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if f, isNum := m["index"].(float64); isNum && int(f) == index {
			line := fmt.Sprintf("已把 %s 的参数 %q 设为 %.4f（回读 %.4f）",
				pluginWhereText(track, slot), mapString(m, "name"), value, numOf(m, "value"))
			if t := mapString(m, "text"); t != "" {
				line += "，显示为 " + t
			}
			return line
		}
	}
	return fmt.Sprintf("已把 %s 的参数 %q 设为 %.4f", pluginWhereText(track, slot), ref, value)
}

// mmListText 列出已安装的 MM 插件（注册表视角：含能力与 AI 标记）
func mmListText() string {
	list := soundlib.ListInstalledMM()
	if len(list) == 0 {
		return "本机还没有安装 MM 插件（Library/plugins 为空）。可以先用 mm_create_plugin → mm_build_plugin → mm_install_plugin 做一个。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "已安装 %d 个 MM 插件：\n", len(list))
	for _, item := range list {
		id := mapString(item, "id")
		line := "  · " + id
		if name := mapString(item, "name"); name != "" {
			line += "（" + name + "）"
		}
		if v := mapString(item, "version"); v != "" {
			line += " v" + v
		}
		if caps, ok := item["capabilities"].([]any); ok && len(caps) > 0 {
			parts := make([]string, 0, len(caps))
			for _, c := range caps {
				parts = append(parts, fmt.Sprint(c))
			}
			line += " 能力=" + strings.Join(parts, ",")
		}
		if origin := mapString(item, "origin"); origin == "ai" {
			line += " ✨AI 生成"
		}
		if cat := mapString(item, "category"); cat != "" {
			line += " 类别=" + cat
		}
		if lib := mapString(item, "libPath"); lib != "" {
			line += "\n      libPath=" + lib
		} else {
			line += "\n      （未找到原生库：包可能不完整，需重新安装）"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("装载：效果器 load_plugin(id=<插件 id>, format=\"mm\", path=<libPath>, slot=0~7)；")
	b.WriteString("乐器 load_plugin(id=<插件 id>, format=\"mm\", path=<libPath>) 不带 slot。")
	return b.String()
}

// arrOf 把 any 安全转成 []any（缺字段/类型不符时给空切片）
func arrOf(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return nil
}

// numOf 安全读数值字段（缺字段或类型不符给 0）
func numOf(m map[string]any, key string) float64 {
	if f, ok := m[key].(float64); ok {
		return f
	}
	return 0
}

// afterText 写操作后的回执：带上最新状态，AI 不必再查一次
func afterText(projectID, tool string, args map[string]any) string {
	switch tool {
	case "add_rack_item", "remove_rack_item":
		return listRackText(projectID)
	case "arrange_track", "set_track_instrument":
		return listTracksText(projectID)
	case "set_track_mix", "set_fx_slot", "set_master_gain":
		/* 混音类操作回执给混音台状态（含声像/静音/FX 槽），
		   而不是轨道清单——后者看不到 pan 与槽位 */
		return mixerText(projectID)
	}
	return ""
}

/* ═══════════ 读取与格式化工具 ═══════════ */

func loadArrangement(projectID string) map[string]any {
	arr := project.LoadArrangementV2(projectID)
	if arr == nil {
		arr = project.NewArrangement()
	}
	return arr
}

func arrangementRackItems(arr map[string]any) []any {
	rack, _ := arr["rack"].(map[string]any)
	if rack == nil {
		return nil
	}
	items, _ := rack["items"].([]any)
	return items
}

func arrangementTracks(arr map[string]any) []any {
	tracks, _ := arr["tracks"].([]any)
	return tracks
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	case float64:
		// 工具参数常把序号写成数字，统一成字符串交给 ops 解析
		if s == float64(int64(s)) {
			return fmt.Sprintf("%d", int64(s))
		}
		return fmt.Sprintf("%v", s)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", s))
	}
}

// requireTrack 取轨道定位参数（支持数字序号）
func requireTrack(args map[string]any) (string, error) {
	track := stringArg(args, "track")
	if track == "" {
		return "", fmt.Errorf("缺少 track（轨道序号从 0 开始 / 轨道 id / 轨道名）")
	}
	return track, nil
}

// trackRef 轨道定位转成 ops 可解析的值：纯数字走序号，其余走 id/名称
func trackRef(track string) any {
	var n int
	if _, err := fmt.Sscanf(track, "%d", &n); err == nil && fmt.Sprintf("%d", n) == track {
		return n
	}
	return track
}

func isPianoRollRef(track string) bool {
	switch strings.ToLower(track) {
	case "pianoroll", "piano_roll", "piano roll", "钢琴窗", "卷帘":
		return true
	}
	return false
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func mapBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

func mapFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

func humanSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%dKB", bytes/(1<<10))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
