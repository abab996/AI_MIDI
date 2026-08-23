package llm

import (
	"fmt"
	"strings"
)

// ToolArgSummaryLimit 工具参数摘要截断长度（与 Python _TOOL_ARG_SUMMARY_LIMIT 一致）
const ToolArgSummaryLimit = 80

// TruncateArgValue 截断工具参数值：超长时截断并标注总字符数
func TruncateArgValue(v any) string {
	s := fmt.Sprintf("%v", v)
	if len(s) <= ToolArgSummaryLimit {
		return s
	}
	return s[:ToolArgSummaryLimit] + fmt.Sprintf("…（共 %d 字符）", len(s))
}

// FormatSingleToolEntry 格式化单个工具调用为可折叠 HTML details 标签。
//
// 与 Python _format_single_tool_entry 行为一致：
//   - summary 显示工具名 + 参数摘要（每个值截断到 80 字符）
//   - 任一参数超长时展开区补一份完整参数代码块
//   - result 放在代码块中
func FormatSingleToolEntry(tcName string, tcArgs map[string]any, resultText string) string {
	var shortParts []string
	hasLong := false
	if tcArgs != nil {
		for k, v := range tcArgs {
			s := fmt.Sprintf("%v", v)
			if len(s) > ToolArgSummaryLimit {
				hasLong = true
			}
			shortParts = append(shortParts, fmt.Sprintf("%s=%s", k, TruncateArgValue(v)))
		}
	}

	var fullArgsBlock string
	if hasLong {
		var lines []string
		for k, v := range tcArgs {
			lines = append(lines, fmt.Sprintf("%s: %v", k, v))
		}
		fullArgsBlock = fmt.Sprintf("**完整参数:**\n```\n%s\n```\n\n", strings.Join(lines, "\n"))
	}

	return fmt.Sprintf(
		"<details>\n<summary>🔧 调用 `%s(%s)`</summary>\n\n%s```\n%s\n```\n</details>",
		tcName, strings.Join(shortParts, ", "), fullArgsBlock, resultText,
	)
}

// FormatPendingToolEntry 工具「执行中」占位块。
//
// 与 Python _format_pending_tool_entry 一致：summary 与完成块相同（含参数），
// 末尾加 ⏳ 标记，正文显示执行状态（class=tool-pending）。
// 前端据此跳过节流并即时显示工具开始执行的反馈。
func FormatPendingToolEntry(tcName string, tcArgs map[string]any) string {
	var shortParts []string
	if tcArgs != nil {
		for k, v := range tcArgs {
			shortParts = append(shortParts, fmt.Sprintf("%s=%s", k, TruncateArgValue(v)))
		}
	}

	return fmt.Sprintf(
		"<details>\n<summary>🔧 调用 `%s(%s)` ⏳</summary>\n\n<div class=\"tool-pending\">⏳ 正在执行 `%s`…</div>\n</details>",
		tcName, strings.Join(shortParts, ", "), tcName,
	)
}
