package llm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"aimidi/internal/config"
)

func TestConvertAnthropicMessages(t *testing.T) {
	msgs := []ChatCompletionMessage{
		{Role: "system", Content: "你是乐理专家"},
		{Role: "user", Content: "写一段旋律"},
		{Role: "assistant", Content: "好", ToolCalls: []ToolCall{{
			ID:   "toolu_1",
			Type: "function",
			Function: FunctionCall{
				Name:      "write_midi",
				Arguments: `{"note":"C4"}`,
			},
		}}},
		{Role: "tool", ToolCallID: "toolu_1", Name: "write_midi", Content: "已写入"},
		{Role: "user", Content: "谢谢"},
	}
	system, out := convertAnthropicMessages(msgs)
	if !strings.Contains(system, "乐理专家") {
		t.Fatalf("system = %q", system)
	}
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, user)", len(out))
	}
	if out[0].Role != "user" || out[1].Role != "assistant" || out[2].Role != "user" {
		t.Fatalf("roles = %s %s %s", out[0].Role, out[1].Role, out[2].Role)
	}
	raw, _ := json.Marshal(out[1].Content)
	if !strings.Contains(string(raw), "tool_use") || !strings.Contains(string(raw), "write_midi") {
		t.Fatalf("assistant content = %s", raw)
	}
	raw, _ = json.Marshal(out[2].Content)
	if !strings.Contains(string(raw), "tool_result") || !strings.Contains(string(raw), "谢谢") {
		t.Fatalf("trailing user should merge tool_result and text, got %s", raw)
	}
}

func TestTranslateAnthropicSSE(t *testing.T) {
	in := strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"ask_user_question"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"想"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	var buf bytes.Buffer
	if err := TranslateAnthropicSSE(strings.NewReader(in), &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `"content":"你好"`) {
		t.Fatalf("missing text: %s", out)
	}
	if !strings.Contains(out, `"reasoning_content":"想"`) {
		t.Fatalf("missing thinking: %s", out)
	}
	if !strings.Contains(out, "ask_user_question") || !strings.Contains(out, `\"q\":1`) && !strings.Contains(out, `{"q":1}`) {
		t.Fatalf("missing tool args: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing finish: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("missing done: %s", out)
	}
}

func TestGeminiProtocolSanitizesOffHost(t *testing.T) {
	s := config.Settings{
		BaseURL:  "https://proxy.example.com",
		Protocol: config.ProtocolGemini,
		Model:    "gemini-2.5-pro",
		Providers: []config.Provider{{
			ID:       "p1",
			Protocol: config.ProtocolGemini,
			BaseURL:  "https://proxy.example.com",
			Enabled:  true,
			Models:   []config.ModelConfig{{ID: "gemini-2.5-pro"}},
		}},
		ActiveProviderID: "p1",
		ActiveModelID:    "gemini-2.5-pro",
	}
	resolved, err := config.ResolveCall(s)
	if err != nil {
		t.Fatal(err)
	}
	if config.EffectiveProtocol(resolved) != config.ProtocolGemini {
		t.Fatalf("protocol = %s", resolved.Protocol)
	}
	msgs := SanitizeMessages([]ChatCompletionMessage{{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:       "c1",
			Type:     "function",
			Function: FunctionCall{Name: "ping", Arguments: "{}"},
		}},
	}}, true)
	sig := msgs[0].ToolCalls[0].ExtraContent
	if sig == nil || sig.Google["thought_signature"] == "" {
		t.Fatal("gemini protocol should attach thought_signature")
	}
}

// Anthropic 只有一个输出上限（max_tokens）。模型目录给 Claude 系填的是
// MaxTokens(8192)，生成页的全局"最大输出"存在 OpenAI 专有的
// max_completion_tokens 里；若后者优先，用户切到 Claude 就会把 32768 发出去 —
// 超过老模型上限，上游直接 400。
func TestAnthropicPrefersMaxTokensOverMaxCompletion(t *testing.T) {
	big, small := 32768, 8192
	s := config.Settings{
		BaseURL:             "https://api.anthropic.com",
		APIPath:             "/v1",
		Protocol:            config.ProtocolAnthropic,
		Model:               "claude-sonnet-4-5-20250929",
		MaxTokens:           &small,
		MaxCompletionTokens: &big,
		Providers: []config.Provider{{
			ID:       "p1",
			Protocol: config.ProtocolAnthropic,
			BaseURL:  "https://api.anthropic.com",
			APIPath:  "/v1",
			Enabled:  true,
			Models:   []config.ModelConfig{{ID: "claude-sonnet-4-5-20250929", MaxTokens: &small}},
		}},
		ActiveProviderID: "p1",
		ActiveModelID:    "claude-sonnet-4-5-20250929",
	}
	resolved, err := config.ResolveCall(s)
	if err != nil {
		t.Fatal(err)
	}
	req := buildAnthropicRequest(resolved, []ChatCompletionMessage{{Role: "user", Content: "hi"}}, nil, false, false)
	if req.MaxTokens != small {
		t.Fatalf("max_tokens = %d, want %d（不能用 max_completion_tokens 盖掉模型建议值）", req.MaxTokens, small)
	}
}

// 两个都没设时仍要落回默认值——Anthropic 的 max_tokens 是必填字段。
func TestAnthropicTokenLimitFallsBackToDefault(t *testing.T) {
	s := config.Settings{Model: "claude-sonnet"}
	req := buildAnthropicRequest(s, []ChatCompletionMessage{{Role: "user", Content: "hi"}}, nil, false, false)
	if req.MaxTokens != config.DefaultMaxTokens {
		t.Fatalf("max_tokens = %d, want %d", req.MaxTokens, config.DefaultMaxTokens)
	}
}
