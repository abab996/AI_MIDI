package llm

import (
	"fmt"
	"strings"
)

// ExtractThinkBlocks 从正文中提取所有 <think>...</think> 片段并剔除标签
func ExtractThinkBlocks(content string) (string, []string) {
	var answerParts []string
	var thinkParts []string
	rest := content

	for {
		start := strings.Index(rest, "<think>")
		if start == -1 {
			answerParts = append(answerParts, rest)
			break
		}
		answerParts = append(answerParts, rest[:start])
		tail := rest[start+len("<think>"):]
		end := strings.Index(tail, "</think>")
		if end == -1 {
			thinkParts = append(thinkParts, strings.TrimSpace(tail))
			break
		}
		thinkParts = append(thinkParts, strings.TrimSpace(tail[:end]))
		rest = tail[end+len("</think>"):]
	}

	return strings.Join(answerParts, ""), thinkParts
}

func cleanReasoning(text string) string {
	s := strings.TrimSpace(text)
	s = strings.TrimLeft(s, "🧠")
	return strings.TrimSpace(s)
}

// FormatDisplayMessage 结合思考/推理过程和回答正文，构造前端 Markdown HTML 展示文本
func FormatDisplayMessage(reasoning string, content string) string {
	answer, thinkParts := ExtractThinkBlocks(content)

	var parts []string
	merged := cleanReasoning(reasoning)
	if merged != "" {
		parts = append(parts, merged)
	}

	for _, tp := range thinkParts {
		p := cleanReasoning(tp)
		if p == "" {
			continue
		}
		already := false
		for _, existing := range parts {
			if existing == p || (len(p) >= 20 && strings.Contains(existing, p)) {
				already = true
				break
			}
		}
		if !already {
			parts = append(parts, p)
		}
	}

	var res strings.Builder
	for _, part := range parts {
		res.WriteString("<details>\n<summary>思考过程</summary>\n\n")
		res.WriteString(part)
		res.WriteString("\n</details>\n\n")
	}
	res.WriteString(answer)

	return res.String()
}

// SplitContentBlocks 将 chunk.content 拆分为 (reasoning, content)
func SplitContentBlocks(value any) (string, string) {
	if value == nil {
		return "", ""
	}
	if s, ok := value.(string); ok {
		return "", s
	}

	if list, ok := value.([]any); ok {
		var reasoning, content strings.Builder
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				bType, _ := m["type"].(string)
				if bType == "thinking" || bType == "reasoning" {
					if txt, ok := m["thinking"].(string); ok {
						reasoning.WriteString(txt)
					} else if txt, ok := m["reasoning"].(string); ok {
						reasoning.WriteString(txt)
					} else if txt, ok := m["content"].(string); ok {
						reasoning.WriteString(txt)
					} else if txt, ok := m["text"].(string); ok {
						reasoning.WriteString(txt)
					}
				} else {
					if txt, ok := m["text"].(string); ok {
						content.WriteString(txt)
					} else if txt, ok := m["content"].(string); ok {
						content.WriteString(txt)
					} else if txt, ok := m["output_text"].(string); ok {
						content.WriteString(txt)
					}
				}
			} else if s, ok := item.(string); ok {
				content.WriteString(s)
			}
		}
		return reasoning.String(), content.String()
	}

	return "", fmt.Sprintf("%v", value)
}
