package chat

import (
	"fmt"
	"strings"

	"aimidi/internal/config"
	"aimidi/internal/llm"
)

// ShouldCompact 判定是否需要执行上下文压缩
func ShouldCompact(messages []llm.ChatCompletionMessage) bool {
	totalChars := 0
	for _, m := range messages {
		totalChars += len(m.Content)
	}
	return totalChars > config.MaxContextChars
}

// CompactFullHistory 压缩完整历史，保留最近 N 轮对话，旧内容替换为摘要
func CompactFullHistory(fullHistory []map[string]any) []map[string]any {
	var userIndices []int
	for i, m := range fullHistory {
		if r, ok := m["role"].(string); ok && r == "user" {
			userIndices = append(userIndices, i)
		}
	}

	if len(userIndices) <= config.CompactKeepRecentMessages {
		return fullHistory
	}

	keepFrom := userIndices[len(userIndices)-config.CompactKeepRecentMessages]
	oldMessages := fullHistory[:keepFrom]
	recentMessages := fullHistory[keepFrom:]

	summary := generateSummary(oldMessages)
	ack := map[string]any{
		"role":    "assistant",
		"content": "好的，我已经了解之前的对话背景与早期上下文摘要。请继续说明具体需求。",
	}

	var compacted []map[string]any
	compacted = append(compacted, summary, ack)
	compacted = append(compacted, recentMessages...)

	return compacted
}

func generateSummary(oldMessages []map[string]any) map[string]any {
	var lines []string
	for _, msg := range oldMessages {
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		content = strings.ReplaceAll(content, "\n", " ")

		if role == "user" && content != "" {
			runes := []rune(content)
			snippet := string(runes)
			if len(runes) > config.SummaryUserTruncateChars {
				snippet = string(runes[:config.SummaryUserTruncateChars])
			}
			lines = append(lines, fmt.Sprintf("- 用户: %s...", snippet))
		} else if role == "assistant" && content != "" {
			runes := []rune(content)
			snippet := string(runes)
			if len(runes) > config.SummaryAITruncateChars {
				snippet = string(runes[:config.SummaryAITruncateChars])
			}
			lines = append(lines, fmt.Sprintf("  AI: %s...", snippet))
		}
	}

	summaryText := "[上下文摘要 — 早期对话已压缩]\n以下是之前对话的摘要，你只需知道之前讨论过以下内容即可:\n" + strings.Join(lines, "\n")
	return map[string]any{
		"role":    "user",
		"content": summaryText,
	}
}
