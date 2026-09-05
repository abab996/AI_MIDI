package chat

import (
	"strings"
	"testing"

	"aimidi/internal/config"
	"aimidi/internal/llm"
)

// TestCloneHistorySnapshotStaysIntact 复核撤回快照损坏 bug 的修复：
// 快照存浅引用时，截断后继续 append 会原地覆写底层数组槽位，恢复出来的
// 是「旧前缀+新消息」的混合体。CloneHistory 生成的快照必须与活数据完全隔离。
func TestCloneHistorySnapshotStaysIntact(t *testing.T) {
	history := []map[string]any{
		{"role": "user", "content": "问题一"},
		{"role": "assistant", "content": "回答一"},
		{"role": "user", "content": "问题二"},
		{"role": "assistant", "content": "回答二"},
	}
	// 编辑第 3 条（index 2）：截断到 histIdx+1 = 3
	histIdx := 2
	snapshot := CloneHistory(history)
	truncated := history[:histIdx+1]

	// 模拟流式 append：cap 足够时原地写穿快照槽位（修复前的事故路径）
	truncated = append(truncated, map[string]any{"role": "assistant", "content": "新生成"})

	if len(snapshot) != len(history) {
		t.Fatalf("快照长度不应受活切片截断/追加影响: %d != %d", len(snapshot), len(history))
	}
	if got, _ := snapshot[3]["content"].(string); got != "回答二" {
		t.Fatalf("快照第 4 条被 append 覆写污染: %q", got)
	}

	// 嵌套结构（tool_calls）也必须深拷贝
	nested := []map[string]any{{
		"role": "assistant",
		"tool_calls": []map[string]any{{
			"id": "tc1",
			"function": map[string]any{
				"name":      "create_midi",
				"arguments": `{"filename":"a.mid"}`,
			},
		}},
	}}
	snap2 := CloneHistory(nested)
	fnMap := snap2[0]["tool_calls"].([]map[string]any)[0]["function"].(map[string]any)
	fnMap["arguments"] = `{"hacked":true}`
	orig := nested[0]["tool_calls"].([]map[string]any)[0]["function"].(map[string]any)
	if orig["arguments"] != `{"filename":"a.mid"}` {
		t.Fatalf("嵌套 map 被就地修改穿透快照: %v", orig["arguments"])
	}

	// CloneDisplay 同样隔离
	display := []map[string]any{{"role": "user", "content": "hi"}}
	ds := CloneDisplay(display)
	display[0]["content"] = "changed"
	if ds[0]["content"] != "hi" {
		t.Fatalf("CloneDisplay 未隔离: %v", ds[0]["content"])
	}
}

// TestShouldCompactCountsToolCallArgs create_midi 的 note_table 存放在
// tool_calls[].function.arguments 里，不计入会让压缩永远不触发直到 400。
func TestShouldCompactCountsToolCallArgs(t *testing.T) {
	// 超过 MaxContextChars（400000）的 arguments：仅存在于 tool_calls 中，
	// Content 为空——修复前 totalChars=0，压缩永不触发
	bigArgs := strings.Repeat("a", config.MaxContextChars+100)
	msgs := []llm.ChatCompletionMessage{{
		Role:    "assistant",
		Content: "",
		ToolCalls: []llm.ToolCall{{
			ID:       "tc1",
			Type:     "function",
			Function: llm.FunctionCall{Name: "create_midi", Arguments: bigArgs},
		}},
	}}
	if !ShouldCompact(msgs) {
		t.Fatalf("tool_calls arguments 体积应计入压缩判定")
	}

	if ShouldCompact([]llm.ChatCompletionMessage{{Role: "user", Content: "短消息"}}) {
		t.Fatalf("短对话不应触发压缩")
	}
}

// TestMapToToolCallListSparseIndex 上游可能用 1-based 或跳号 index，
// 按下标连续遍历会静默丢弃尾部工具调用。
func TestMapToToolCallListSparseIndex(t *testing.T) {
	m := map[int]*llm.ToolCall{
		1: {Index: 1, ID: "b"},
		3: {Index: 3, ID: "d"},
		0: {Index: 0, ID: "a"},
	}
	list := mapToToolCallList(m)
	if len(list) != 3 {
		t.Fatalf("期望 3 个工具调用，实际 %d 个", len(list))
	}
	wantOrder := []string{"a", "b", "d"}
	for i, w := range wantOrder {
		if list[i].ID != w {
			t.Fatalf("顺序错误: [%d]=%s, 期望 %s", i, list[i].ID, w)
		}
	}
	if mapToToolCallList(nil) != nil {
		t.Fatalf("空 map 应返回 nil")
	}
}
