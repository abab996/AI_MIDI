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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"melodymate/internal/config"
	"melodymate/internal/llm"
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
			Name:        "mm_build_plugin",
			Description: "编译 MM 插件工程并打包 .mm（调 mm build：配置+编译+收集+写哈希+打包+校验）。**首次编译必须先把要做的改动告诉用户并获得确认**，然后带 confirm=true 调用；返回编译日志尾部。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "string", "description": "插件 id"},
					"confirm": map[string]any{"type": "boolean", "description": "用户已确认本次编译（首次必须为 true）"},
				},
				"required": []string{"id", "confirm"},
			},
		},
	},
	{
		Type: "function",
		Function: llm.FunctionSchema{
			Name:        "mm_install_plugin",
			Description: "把编译好的 .mm 安装到 <数据根>/Library/plugins/（校验 + 能力授权 fail-closed）。安装后可在机架/混音台里加载。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":    map[string]any{"type": "string", "description": "插件 id"},
					"grant": map[string]any{"type": "string", "description": "用户授权的能力，逗号分隔（如 files,network）；插件没声明而这里给了会被拒绝"},
				},
				"required": []string{"id"},
			},
		},
	},
}

func isMMTool(name string) bool {
	switch name {
	case "mm_create_plugin", "mm_write_file", "mm_build_plugin", "mm_install_plugin":
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

func executeMMTool(name string, args map[string]any) (string, error) {
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
		return fmt.Sprintf("已写入 %s（%d 字节）", filepath.ToSlash(filepath.Join(id, clean)), len(content)), nil

	case "mm_build_plugin":
		id := stringArg(args, "id")
		if !mapBool(args, "confirm") {
			return "需要用户确认后才能编译（首次编译会在本机执行 C++ 构建）：请先把要创建的插件作用与参数告诉用户，得到同意后再带 confirm=true 调用 mm_build_plugin。", nil
		}
		dir, err := mmSourceDir(id)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr != nil {
			return "错误：工程不存在（先 mm_create_plugin）: " + dir, nil
		}
		sdk := mmSDKRoot()
		mmArgs := []string{"build", dir}
		if sdk != "" {
			mmArgs = append(mmArgs, "--sdk", sdk)
		}
		out, err := runMM(mmArgs...)
		if err != nil {
			return "编译失败：\n" + out, nil
		}
		return "编译成功：\n" + out + "\n下一步：mm_install_plugin（需要时先与用户确认能力清单）", nil

	case "mm_install_plugin":
		id := stringArg(args, "id")
		grant := strings.TrimSpace(stringArg(args, "grant"))
		dir, err := mmSourceDir(id)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*.mm"))
		if len(matches) == 0 {
			return "错误：没有找到 .mm 包（先 mm_build_plugin）: " + dir, nil
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
		return out + "\n已安装到 " + dest + "；可在设置页「声音管理」或机架/混音台里加载。", nil
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
