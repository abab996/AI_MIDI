package mcp

// MM 插件平台的 AI 闭环工具（P6）：
//
//	mm_create_plugin → mm_write_file → mm_build_plugin → mm_install_plugin
//
// 设计约束（规范 §9 / §4 能力模型）：
//   · 源码根固定为 <数据根>/Library/plugin-src/<id>/，写文件逐条路径校验
//     （拒绝 ../、绝对路径、盘符），单文件大小上限；
//   · 编译与安装都调 cmd/mm（规范唯一实现），不在这里重复实现打包逻辑；
//   · **首次编译需要用户确认**：工具要求显式 confirm=true，描述里要求 AI 先问用户；
//   · 安装走能力授权 fail-closed（声明未授权即拒绝，不允许多授权）。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"melodymate/internal/config"
	"melodymate/internal/llm"
	"melodymate/internal/soundlib"
)

const mmMaxSourceBytes = 512 << 10 // 单文件 512 KiB

var mmIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,127}$`)

var mmTools = []llm.ToolDefinition{
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_create_plugin",
			Description: "创建一个 MM 插件工程（脚手架：CMakeLists + src/plugin.cpp + manifest.json + ui/index.html）。之后用 mm_write_file 写源码、mm_build_plugin 编译。id 用反向域名（如 com.example.myfilter）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":   map[string]any{"type": "string", "description": "插件 id（反向域名，小写）"},
					"name": map[string]any{"type": "string", "description": "显示名（≤64 字符）"},
					"kind": map[string]any{"type": "string", "enum": []string{"effect", "instrument"}, "description": "effect=效果器（挂 FX 槽）；instrument=乐器（挂轨道）"},
				},
				"required": []string{"id", "name", "kind"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_write_file",
			Description: "写入 MM 插件工程内的文件（相对工程根，如 src/plugin.cpp、manifest.json、ui/index.html）。路径必须在工程目录内。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "string", "description": "插件 id"},
					"path":    map[string]any{"type": "string", "description": "工程内相对路径（用 / 分隔）"},
					"content": map[string]any{"type": "string", "description": "完整文件内容（覆盖写入）"},
				},
				"required": []string{"id", "path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_request_build_approval",
			Description: "在编译或安装 MM 插件前，向用户发起界面确认（批准卡）。必须先用本工具取得 approval_id，用户点击允许后，再把该 approval_id 传给 mm_build_plugin / mm_install_plugin。**模型自己无法代替用户授权**：不调用本工具就直接编译/安装会被拒绝。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "string", "description": "插件 id"},
					"tool":   map[string]any{"type": "string", "enum": []string{"mm_build_plugin", "mm_install_plugin"}, "description": "要为哪个操作请求确认"},
					"grant":  map[string]any{"type": "string", "description": "tool=mm_install_plugin 时：请求授权的能力，逗号分隔（如 files,network）"},
					"reason": map[string]any{"type": "string", "description": "向用户说明这次编译/安装要做什么（会显示在批准卡上）"},
				},
				"required": []string{"id", "tool", "reason"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_build_plugin",
			Description: "编译 MM 插件工程并打包 .mm（调 mm build：配置+编译+收集+写哈希+打包+校验）。**必须先调用 mm_request_build_approval 取得用户确认**，再带返回的 approval_id 调用本工具；没有有效 approval_id 会被拒绝。返回编译日志尾部。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "description": "插件 id"},
					"approval_id": map[string]any{"type": "string", "description": "mm_request_build_approval 返回的批准编号（用户点击允许后有效）"},
				},
				"required": []string{"id", "approval_id"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_build_status",
			Description: "查看 mm_build_plugin 的编译进度（长编译用）。返回阶段（配置/编译/收集/打包/校验）、已用时与日志尾部。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"job": map[string]any{"type": "string", "description": "mm_build_plugin 返回的作业号"},
				},
				"required": []string{"job"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_install_plugin",
			Description: "把编译好的 .mm 安装到 <数据根>/Library/plugins/（校验 + 能力授权 fail-closed）。**必须先调用 mm_request_build_approval（tool=mm_install_plugin）取得用户确认**，再带 approval_id 调用本工具。安装后可在机架/混音台里加载。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "description": "插件 id"},
					"grant":       map[string]any{"type": "string", "description": "用户授权的能力，逗号分隔（如 files,network）；插件没声明而这里给了会被拒绝"},
					"approval_id": map[string]any{"type": "string", "description": "mm_request_build_approval 返回的批准编号"},
				},
				"required": []string{"id", "approval_id"},
			},
		},
	},
}

func isMMTool(name string) bool {
	switch name {
	case "mm_create_plugin", "mm_write_file", "mm_build_plugin", "mm_build_status",
		"mm_install_plugin", "mm_request_build_approval":
		return true
	}
	return false
}

// mmSourceDir 插件源码根（绝对路径，逐条校验后使用）
func mmSourceDir(id string) (string, error) {
	if !mmIDPattern.MatchString(id) {
		return "", fmt.Errorf("插件 id 非法（反向域名，小写字母/数字/./-）: %s", id)
	}
	root := filepath.Join(config.LibraryDir, "plugin-src")
	dir := filepath.Join(root, id)
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("插件目录越界")
	}
	return dir, nil
}

// mmExePath 定位 mm.exe（构建为 mm.exe，随安装包分发；开发期在仓库 output/ 下）
func mmExePath() (string, error) {
	if v := strings.TrimSpace(os.Getenv("MM_EXE")); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v, nil
		}
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "mm.exe"),
			filepath.Join(dir, "tools", "mm.exe"),
			filepath.Join(dir, "output", "mm.exe"),
			filepath.Join(dir, "..", "output", "mm.exe"),
		)
	}
	candidates = append(candidates, "mm.exe", "mm")
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("找不到 mm.exe：请设置环境变量 MM_EXE，或把它与主程序放在同一目录（发行包会带上）")
}

// runMM 执行 mm 子命令并返回合并输出（截尾，避免超长日志灌进对话）
func runMM(args ...string) (string, error) {
	exe, err := mmExePath()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, args...)
	out, runErr := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\r\n ")
	const maxChars = 6000
	if len(text) > maxChars {
		text = "…（前段省略）\n" + text[len(text)-maxChars:]
	}
	if runErr != nil {
		if text == "" {
			text = runErr.Error()
		}
		return text, fmt.Errorf("%s", text)
	}
	return text, nil
}

func executeMMTool(env ToolEnv, name string, args map[string]any) (string, error) {
	switch name {
	case "mm_create_plugin":
		id := stringArg(args, "id")
		nm := stringArg(args, "name")
		kind := strings.ToLower(strings.TrimSpace(stringArg(args, "kind")))
		if kind != "effect" && kind != "instrument" {
			return "错误：kind 必须是 effect 或 instrument", nil
		}
		dir, err := mmSourceDir(id)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr == nil {
			return "插件工程已存在：" + dir + "（可直接 mm_write_file 修改后 mm_build_plugin）", nil
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "错误：" + err.Error(), nil
		}
		out, err := runMM("new", dir, "--kind", kind, "--name", nm, "--id", id)
		if err != nil {
			return "错误：mm new 失败：" + out, nil
		}
		files, _ := os.ReadDir(dir)
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Name())
		}
		return fmt.Sprintf("已创建 MM 插件工程（%s / %s）：\n%s\n\n文件：%s\n下一步：用 mm_write_file 写 src/plugin.cpp 与 manifest.json，然后 mm_build_plugin（首次会要求用户确认）。",
			id, kind, dir, strings.Join(names, ", ")), nil

	case "mm_write_file":
		id := stringArg(args, "id")
		rel := strings.TrimSpace(stringArg(args, "path"))
		content := stringField(args, "content")
		dir, err := mmSourceDir(id)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		if rel == "" {
			return "错误：缺少 path", nil
		}
		clean := filepath.ToSlash(filepath.Clean(rel))
		if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") || regexp.MustCompile(`^[A-Za-z]:`).MatchString(clean) {
			return "错误：路径非法（必须是工程内相对路径）: " + rel, nil
		}
		if len(content) > mmMaxSourceBytes {
			return fmt.Sprintf("错误：文件过大（%d 字节，上限 %d）", len(content), mmMaxSourceBytes), nil
		}
		full := filepath.Join(dir, filepath.FromSlash(clean))
		if r, rerr := filepath.Rel(dir, full); rerr != nil || strings.HasPrefix(r, "..") {
			return "错误：路径越界: " + rel, nil
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "错误：" + err.Error(), nil
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return "错误：" + err.Error(), nil
		}
		reply := fmt.Sprintf("已写入 %s（%d 字节）", filepath.ToSlash(filepath.Join(id, clean)), len(content))
		if env.Approval != nil {
			/* 在途批准绑定签发时的工程内容摘要：内容一变摘要即失效，
			   这里主动作废本工程全部在途批准，逼模型重新走一次用户确认，
			   堵住"批准 A 内容、编译 B 内容"的 TOCTOU 窗口。 */
			env.Approval.InvalidateProject(env.ProjectID)
			reply += "\n该工程的在途批准已作废，需重新发起确认（mm_request_build_approval）。"
		}
		return reply, nil

	case "mm_request_build_approval":
		id := stringArg(args, "id")
		/* 确认的是"编译"还是"安装"：两者的参数摘要不同，令牌不可混用 */
		tool := strings.TrimSpace(stringArg(args, "tool"))
		if tool != "mm_build_plugin" && tool != "mm_install_plugin" {
			return "错误：tool 必须是 mm_build_plugin 或 mm_install_plugin", nil
		}
		dir, err := mmSourceDir(id)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr != nil {
			return "错误：工程不存在（先 mm_create_plugin）: " + dir, nil
		}
		/* 汇总将被编译的文件清单与实际执行的命令——批准卡要让人看清
		   "允许的到底是什么"，而不是只给一句"允许编译吗"。 */
		files := collectPluginFiles(dir)
		/* 内容摘要绑定：批准的不只是"这份工程"，而是"此刻的这份内容"。
		   签发到消费之间任何文件改动（mm_write_file 或外部）都会让摘要
		   对不上，消费时拒绝。 */
		content, cerr := mmProjectContentDigest(dir)
		if cerr != nil {
			return "错误：无法读取工程内容（" + cerr.Error() + "）", nil
		}
		approvedArgs := map[string]any{"id": id, "content": content}
		if tool == "mm_install_plugin" {
			approvedArgs["grant"] = strings.TrimSpace(stringArg(args, "grant"))
			/* 安装目标在签发时即定死：消费时按同一规则（字典序末位）取包并
			   由摘要保证就是这份——签发后新增/替换 .mm 都会摘要不匹配。 */
			matches, _ := filepath.Glob(filepath.Join(dir, "*.mm"))
			sort.Strings(matches)
			if len(matches) == 0 {
				return "错误：没有找到 .mm 包（先 mm_build_plugin）: " + dir, nil
			}
			approvedArgs["mmFile"] = filepath.Base(matches[len(matches)-1])
		}
		req := ApprovalRequest{
			Tool:       tool,
			ArgsDigest: ArgsDigest(tool, approvedArgs),
			Files:      files,
			Command:    mmBuildCommand(dir),
			Reason:     stringArg(args, "reason"),
		}
		approvalID, aerr := issueApproval(env, newApprovalID(), req)
		if aerr != nil {
			return "错误：无法请求用户确认（" + aerr.Error() + "）", nil
		}
		emitApprovalCard(env, approvalID, req)
		target := "mm_build_plugin"
		action := "编译"
		if tool == "mm_install_plugin" {
			target = "mm_install_plugin"
			action = "安装"
		}
		return fmt.Sprintf("已向用户请求%s确认（approval_id=%s）。**请结束本轮回复、等待用户在界面上点按允许**，"+
			"然后在下一轮带该 approval_id 调用 %s；在用户确认前调用会被拒绝。\n"+
			"工程：%s\n文件：%s\n命令：%s", action, approvalID, target, dir, strings.Join(files, "、"), req.Command), nil

	case "mm_build_plugin":
		id := stringArg(args, "id")
		dir, err := mmSourceDir(id)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr != nil {
			return "错误：工程不存在（先 mm_create_plugin）: " + dir, nil
		}
		/* 授权来自用户点过的那一次确认，而不是模型传的 confirm。
		   参数摘要绑定"批准的就是这份工程的这份内容"：换工程或换内容
		   （批准后改过任何文件）都不能复用同一令牌。 */
		content, cerr := mmProjectContentDigest(dir)
		if cerr != nil {
			return "错误：无法读取工程内容（" + cerr.Error() + "）", nil
		}
		digest := ArgsDigest("mm_build_plugin", map[string]any{"id": id, "content": content})
		if err := consumeApproval(env, stringArg(args, "approval_id"), "mm_build_plugin", digest); err != nil {
			return "需要用户确认后才能编译（首次编译会在本机执行 C++ 构建）：" + err.Error() +
				"。请先调用 mm_request_build_approval 发起确认，用户允许后再带 approval_id 重试。", nil
		}
		/* 后台作业 + 进度报告：快编译直接给结果，慢编译给出进度卡与作业号
		   （同步等待会让对话长时间空白，直接截断又会丢日志） */
		job, jerr := startMMBuild(id, dir, mmSDKRoot())
		if jerr != nil {
			return "错误：" + jerr.Error(), nil
		}
		if waitMMBuild(job, 25*time.Second) {
			return job.report(), nil
		}
		return job.report() + "\n（编译未在 25 秒内完成，已转为后台作业；用 mm_build_status 继续查看）", nil

	case "mm_build_status":
		jobID := stringArg(args, "job")
		job, ok := mmBuildJobByID(jobID)
		if !ok {
			return "错误：找不到编译作业 " + jobID + "（作业号由 mm_build_plugin 返回；重启主程序后作业记录会清空）", nil
		}
		return job.report(), nil

	case "mm_install_plugin":
		id := stringArg(args, "id")
		grant := strings.TrimSpace(stringArg(args, "grant"))
		dir, err := mmSourceDir(id)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		/* 安装同样需要用户确认：安装会把原生库写进 Library/plugins 并在
		   下次加载时执行其代码，授权不能由模型自己声明。
		   .mm 取字典序末位并绑进摘要——与签发时同一规则，签发后新增/
		   替换 .mm 包都会因 mmFile 或内容摘要不一致被拒。 */
		matches, _ := filepath.Glob(filepath.Join(dir, "*.mm"))
		sort.Strings(matches)
		if len(matches) == 0 {
			return "错误：没有找到 .mm 包（先 mm_build_plugin）: " + dir, nil
		}
		content, cerr := mmProjectContentDigest(dir)
		if cerr != nil {
			return "错误：无法读取工程内容（" + cerr.Error() + "）", nil
		}
		installArgs := map[string]any{"id": id, "grant": grant, "content": content, "mmFile": filepath.Base(matches[len(matches)-1])}
		if err := consumeApproval(env, stringArg(args, "approval_id"), "mm_install_plugin", ArgsDigest("mm_install_plugin", installArgs)); err != nil {
			return "需要用户确认后才能安装：" + err.Error() +
				"。请先调用 mm_request_build_approval（tool=mm_install_plugin）发起确认，用户允许后再带 approval_id 重试。", nil
		}
		dest := filepath.Join(config.LibraryDir, "plugins")
		args2 := []string{"install", matches[len(matches)-1], "--dest", dest}
		if grant != "" {
			args2 = append(args2, "--grant", grant)
		}
		out, err := runMM(args2...)
		if err != nil {
			return "安装被拒绝：\n" + out + "\n（能力授权 fail-closed：插件声明了能力就必须由用户确认后再传 grant）", nil
		}
		/* 同步注册表：装完不同步的话，插件在「声音管理」与素材库「音源」页签里
		   都看不到（用户只能靠命令行知道装了什么）。AI 生成的插件同时带上
		   ✨AI 徽标（origin=ai）。 */
		regLine := ""
		if entries, rerr := os.ReadDir(dest); rerr == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if e.Name() == id || strings.HasPrefix(e.Name(), id+"@") {
					if src, created, serr := soundlib.SyncInstalledMM(filepath.Join(dest, e.Name())); serr == nil {
						action := "已登记"
						if !created {
							action = "已更新登记"
						}
						regLine = "\n音源注册表：" + action + " " + src.Name + "（kind=mm，origin=" + src.Origin + "）"
						if src.AI != nil {
							regLine += " —— 声音管理/素材库里会显示 ✨AI 徽标"
						}
					} else {
						regLine = "\n（注册表同步失败：" + serr.Error() + "）"
					}
					break
				}
			}
		}
		return out + "\n已安装到 " + dest + "；可在设置页「声音管理」或机架/混音台里加载。" + regLine, nil
	}
	return "错误：未知工具 " + name, nil
}

// mmSDKRoot 找 SDK（引擎仓库 mm-sdk；发行包可放在 <数据根>/../mm-sdk 或环境变量）
func mmSDKRoot() string {
	if v := strings.TrimSpace(os.Getenv("MM_SDK_ROOT")); v != "" {
		return v
	}
	var bases []string
	if exe, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(exe))
	}
	for _, b := range bases {
		for _, rel := range []string{
			filepath.Join("engine", "mm-sdk"),
			filepath.Join("..", "engine", "mm-sdk"),
			filepath.Join("..", "..", "engine", "mm-sdk"),
		} {
			p := filepath.Join(b, rel)
			if _, err := os.Stat(filepath.Join(p, "include", "mm", "mm.h")); err == nil {
				return p
			}
		}
	}
	return ""
}

// stringField 取字符串字段（缺失返回空串；与 stringArg 的区别：不 trim）
func stringField(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

/* ═══════════ 用户确认闸门（编译/安装的授权来源） ═══════════ */

// newApprovalID 生成一次性批准令牌编号
func newApprovalID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// 随机源不可用时退化为时间戳（仍是一次性、仍绑定参数摘要）
		return fmt.Sprintf("appr-%d", time.Now().UnixNano())
	}
	return "appr-" + hex.EncodeToString(buf)
}

// issueApproval 登记待批准请求，返回**生效的令牌编号**（对话层可能加
// 项目前缀实现隔离——返回值才是卡片与工具回复该携带的编号）。
// enforceApproval 为 false（未接入界面确认通道，如 stdio MCP）时返回
// 错误——宁可不执行，也不退回"模型自证"。
func issueApproval(env ToolEnv, id string, req ApprovalRequest) (string, error) {
	if env.Approval == nil {
		return "", fmt.Errorf("当前没有可用的用户确认通道")
	}
	if issuer, ok := env.Approval.(interface {
		Issue(string, ApprovalRequest) string
	}); ok {
		return issuer.Issue(id, req), nil
	}
	return "", fmt.Errorf("当前确认通道不支持签发批准令牌")
}

// consumeApproval 校验并消费一次性批准令牌
func consumeApproval(env ToolEnv, approvalID, tool, digest string) error {
	if env.Approval == nil {
		return fmt.Errorf("当前没有可用的用户确认通道")
	}
	return env.Approval.Consume(approvalID, tool, digest)
}

// emitApprovalCard 把批准卡推给前端（复用对话流的 chat 事件通道）
func emitApprovalCard(env ToolEnv, approvalID string, req ApprovalRequest) {
	if env.Emit == nil {
		return
	}
	env.Emit(map[string]any{
		"type": "approval_request",
		"approval": map[string]any{
			"approval_id": approvalID,
			"tool":        req.Tool,
			"files":       req.Files,
			"command":     req.Command,
			"reason":      req.Reason,
		},
	})
}

// collectPluginFiles 列出工程内将被编译的文件（批准卡展示用，相对工程根）
func collectPluginFiles(dir string) []string {
	var files []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// 构建产物不入清单
		if strings.HasPrefix(rel, "build/") || strings.HasPrefix(rel, "bin/") {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if len(files) > 40 {
		files = append(files[:40], fmt.Sprintf("…（共 %d 个文件）", len(files)))
	}
	return files
}

// mmProjectContentDigest 对工程内将被编译/打包的文件逐个算 sha256，
// 按相对路径排序后聚合为一条内容摘要（hex）。批准令牌绑定这条摘要：
// 签发后任何人再改工程内文件（mm_write_file 或外部进程），消费时摘要
// 对不上即拒绝，堵住"批准的是 A 内容、执行的却是 B 内容"的 TOCTOU。
// 与批准卡清单同一来源（排除 build/、bin/）；单文件读取失败直接报错。
func mmProjectContentDigest(dir string) (string, error) {
	var rels []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "build/") || strings.HasPrefix(rel, "bin/") {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	sort.Strings(rels)
	h := sha256.New()
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(h, "%s:%x\n", rel, sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// mmBuildCommand 批准卡上展示的实际命令（与 mm_build_job 保持一致）
func mmBuildCommand(dir string) string {
	return "mm build " + dir + "（编译将执行工程内 CMakeLists.txt——cmake 配置阶段可执行任意命令）"
}
