package llm

import "strings"

// ModelProfile 按模型 ID 前缀给出建议参数。ContextWindow 只用于界面提示。
type ModelProfile struct {
	Match               string `json:"match"`
	MaxTokens           *int   `json:"max_tokens"`
	MaxCompletionTokens *int   `json:"max_completion_tokens"`
	ReasoningEffort     string `json:"reasoning_effort"`
	ThinkingEnabled     *bool  `json:"thinking_enabled"`
	ContextWindow       int    `json:"context_window"`
	/* ReasoningEfforts 这个模型接受哪些思考强度档位（本应用词汇 low/medium/
	   max）。nil = 该模型不收这个参数，界面不显示档位选择，一律沿用「生成」
	   页的全局值。目录里没收录的模型不套用 nil（见 SupportedReasoningEfforts） */
	ReasoningEfforts []string `json:"reasoning_efforts"`
}

// AllReasoningEfforts 本应用支持的思考强度档位（OpenAI 侧对应
// reasoning_effort，Gemini / Anthropic 侧映射到各自的思考预算）。
var AllReasoningEfforts = []string{"low", "medium", "max"}

func n(v int) *int {
	return &v
}

func b(v bool) *bool {
	return &v
}

func eff(v ...string) []string {
	return v
}

// SupportedReasoningEfforts 返回模型可选的思考强度档位。
// 目录里收录的按目录声明走；目录外的返回全档位——用户手里的模型千奇百怪，
// 不该因为目录没收录就让人无从调节。
func SupportedReasoningEfforts(modelID string) []string {
	if p, ok := MatchModelProfile(modelID); ok {
		return p.ReasoningEfforts
	}
	return AllReasoningEfforts
}

// ModelProfiles 查找时按匹配长度取最长的一条。
// 只有明确收 reasoning_effort 的模型才写 ReasoningEfforts，其余留空。
var ModelProfiles = []ModelProfile{
	{Match: "gpt-4.1", MaxCompletionTokens: n(32768), ContextWindow: 1047576},
	{Match: "gpt-4o-mini", MaxCompletionTokens: n(16384), ContextWindow: 128000},
	{Match: "gpt-4o", MaxCompletionTokens: n(16384), ContextWindow: 128000},
	{Match: "o4-mini", MaxCompletionTokens: n(32768), ReasoningEffort: "medium", ThinkingEnabled: b(true), ContextWindow: 200000, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "o3", MaxCompletionTokens: n(32768), ReasoningEffort: "medium", ThinkingEnabled: b(true), ContextWindow: 200000, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "o1", MaxCompletionTokens: n(32768), ReasoningEffort: "medium", ThinkingEnabled: b(true), ContextWindow: 200000, ReasoningEfforts: eff("low", "medium", "max")},

	{Match: "claude-opus", MaxTokens: n(8192), ContextWindow: 200000},
	{Match: "claude-sonnet", MaxTokens: n(8192), ContextWindow: 200000},
	{Match: "claude-haiku", MaxTokens: n(8192), ContextWindow: 200000},

	{Match: "gemini-2.5-pro", MaxCompletionTokens: n(65536), ThinkingEnabled: b(true), ContextWindow: 1048576, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "gemini-2.5-flash", MaxCompletionTokens: n(65536), ThinkingEnabled: b(true), ContextWindow: 1048576, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "gemini-2.0-flash", MaxCompletionTokens: n(8192), ContextWindow: 1048576},

	/* v4 系仍按"可调档位"处理：应用的全局默认推理强度就是 max，档位也一直
	   是三档可选，这里不下调既有行为 */
	{Match: "deepseek-v4-pro", MaxTokens: n(8192), ContextWindow: 128000, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "deepseek-v4-flash", MaxTokens: n(8192), ContextWindow: 128000, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "deepseek-reasoner", MaxTokens: n(8192), ReasoningEffort: "max", ThinkingEnabled: b(true), ContextWindow: 128000, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "deepseek-r1", MaxTokens: n(8192), ReasoningEffort: "max", ThinkingEnabled: b(true), ContextWindow: 128000, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "deepseek-chat", MaxTokens: n(8192), ContextWindow: 128000},

	{Match: "grok-", MaxCompletionTokens: n(16384), ContextWindow: 131072},
	{Match: "qwen-max", MaxTokens: n(8192), ContextWindow: 131072},
	{Match: "qwen-plus", MaxTokens: n(8192), ContextWindow: 131072},
	{Match: "qwen-turbo", MaxTokens: n(8192), ContextWindow: 131072},
	{Match: "qwen3", MaxTokens: n(8192), ContextWindow: 131072, ReasoningEfforts: eff("low", "medium", "max")},
	{Match: "qwen2.5", MaxTokens: n(8192), ContextWindow: 131072},
	{Match: "glm-", MaxTokens: n(8192), ContextWindow: 128000},
	{Match: "moonshot-v1-128k", MaxTokens: n(8192), ContextWindow: 128000},
	{Match: "moonshot-v1-32k", MaxTokens: n(8192), ContextWindow: 32000},
	{Match: "moonshot-v1-8k", MaxTokens: n(4096), ContextWindow: 8000},
	{Match: "kimi", MaxTokens: n(8192), ContextWindow: 128000},
	{Match: "mistral-large", MaxTokens: n(8192), ContextWindow: 128000},
	{Match: "mistral-small", MaxTokens: n(8192), ContextWindow: 128000},
	{Match: "minimax", MaxTokens: n(8192), ContextWindow: 128000},
	{Match: "llama-3", MaxTokens: n(8192), ContextWindow: 128000},
}

// MatchModelProfile 用模型 ID 全名或其最后一段做最长前缀匹配。
func MatchModelProfile(id string) (ModelProfile, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ModelProfile{}, false
	}
	cands := []string{id}
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		cands = append(cands, id[i+1:])
	}
	bestLen := -1
	var best ModelProfile
	found := false
	for _, p := range ModelProfiles {
		m := strings.ToLower(p.Match)
		for _, c := range cands {
			if profileHit(c, m) && len(m) > bestLen {
				best = p
				bestLen = len(m)
				found = true
			}
		}
	}
	return best, found
}

func profileHit(id, match string) bool {
	if match == "" || len(id) < len(match) {
		return false
	}
	if !strings.HasPrefix(id, match) {
		return false
	}
	if len(id) == len(match) || strings.HasSuffix(match, "-") {
		return true
	}
	c := id[len(match)]
	return c == '-' || c == '.' || c == ':' || c == '/'
}
