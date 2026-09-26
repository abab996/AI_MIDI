package llm

import "testing"

func TestEndpointURL(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		path     string
		protocol string
		resource string
		want     string
	}{
		{"deepseek empty path", "https://api.deepseek.com", "", "openai", "chat", "https://api.deepseek.com/v1/chat/completions"},
		{"openai v1", "https://api.openai.com", "/v1", "openai", "chat", "https://api.openai.com/v1/chat/completions"},
		{"zhipu v4", "https://open.bigmodel.cn", "/api/paas/v4", "openai", "chat", "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{"gemini openai compat", "https://generativelanguage.googleapis.com", "/v1beta/openai", "gemini", "chat", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"},
		{"groq", "https://api.groq.com", "/openai/v1", "openai", "chat", "https://api.groq.com/openai/v1/chat/completions"},
		{"perplexity root", "https://api.perplexity.ai", "/", "openai", "chat", "https://api.perplexity.ai/chat/completions"},
		{"anthropic messages", "https://api.anthropic.com", "/v1", "anthropic", "chat", "https://api.anthropic.com/v1/messages"},
		{"anthropic models", "https://api.anthropic.com", "/v1", "anthropic", "models", "https://api.anthropic.com/v1/models"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EndpointURL(tc.base, tc.path, tc.protocol, tc.resource)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
