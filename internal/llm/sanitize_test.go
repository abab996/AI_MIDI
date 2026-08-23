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
