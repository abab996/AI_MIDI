package llm

import (
	"testing"
)

func TestEnsureToolClosureTrailingDangling(t *testing.T) {
	// 场景：最后一条消息为 assistant 且包含未闭合的 tool_calls（如流被中断）
	messages := []ChatCompletionMessage{
		{Role: "user", Content: "帮我写一段旋律"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{
					ID:   "call_123",
					Type: "function",
					Function: FunctionCall{
						Name:      "create_midi",
						Arguments: `{"filename":"melody.mid"}`,
					},
				},
			},
		},
	}

	closed := EnsureToolClosure(messages)
	if len(closed) != 3 {
		t.Fatalf("期望修复后有 3 条消息，实际得到 %d 条", len(closed))
	}

	last := closed[len(closed)-1]
	if last.Role != "tool" {
		t.Fatalf("期望末尾补齐 tool 消息，实际角色为: %s", last.Role)
	}
	if last.ToolCallID != "call_123" {
		t.Fatalf("期望 ToolCallID 为 call_123，实际为: %s", last.ToolCallID)
	}
	if last.Name != "create_midi" {
		t.Fatalf("期望 Name 为 create_midi，实际为: %s", last.Name)
	}
}

func TestEnsureToolClosureNormal(t *testing.T) {
	// 正常闭合的场景
	messages := []ChatCompletionMessage{
		{Role: "user", Content: "你好"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{
					ID:   "call_abc",
					Type: "function",
					Function: FunctionCall{
						Name:      "read_library_file",
						Arguments: `{"name":"test.mid"}`,
					},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_abc",
			Content:    "file content",
			Name:       "read_library_file",
		},
		{
			Role:    "assistant",
			Content: "已读取文件",
		},
	}

	closed := EnsureToolClosure(messages)
	if len(closed) != 4 {
		t.Fatalf("期望正常闭合无需增加消息，实际长度为 %d", len(closed))
	}
}

func TestSanitizeMessagesToolCallArguments(t *testing.T) {
	// 回归测试：针对 OpenAI 400 "Assistant tool call <id>.arguments must be valid JSON."
	// 当流式输出中断、模型提前截断或无参工具输出空字符串时，Arguments 必须被兜底修复为 "{}"
	messages := []ChatCompletionMessage{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{
					ID:       "call_empty",
					Function: FunctionCall{Name: "list_files", Arguments: ""},
				},
				{
					ID:       "call_spaces",
					Function: FunctionCall{Name: "list_files", Arguments: "   "},
				},
				{
					ID:       "call_broken",
					Function: FunctionCall{Name: "read_file", Arguments: `{"filename": "test.md`}, // 截断的非法 JSON
				},
				{
					ID:       "call_valid",
					Function: FunctionCall{Name: "read_file", Arguments: `{"filename":"ok.md"}`},
				},
			},
		},
	}

	sanitized := SanitizeMessages(messages, false)
	if len(sanitized) != 1 || len(sanitized[0].ToolCalls) != 4 {
		t.Fatalf("清洗后的消息或工具调用数量不匹配: %+v", sanitized)
	}

	tcs := sanitized[0].ToolCalls
	if tcs[0].Function.Arguments != "{}" {
		t.Errorf("空 arguments 期望修复为 \"{}\"，实际为 %q", tcs[0].Function.Arguments)
	}
	if tcs[1].Function.Arguments != "{}" {
		t.Errorf("纯空白 arguments 期望修复为 \"{}\"，实际为 %q", tcs[1].Function.Arguments)
	}
	if tcs[2].Function.Arguments != "{}" {
		t.Errorf("非法 JSON arguments 期望修复为 \"{}\"，实际为 %q", tcs[2].Function.Arguments)
	}
	if tcs[3].Function.Arguments != `{"filename":"ok.md"}` {
		t.Errorf("合法 JSON arguments 不应被误修改，实际为 %q", tcs[3].Function.Arguments)
	}
}
