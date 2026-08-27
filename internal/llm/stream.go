package llm

import (
	"fmt"
	"regexp"
	"strings"
)

// thinkTagRe 匹配 <think> 变体：允许大小写、标签内空白与单个属性
// （<THINK> / <think > / <think id="x"> 等）。此前只匹配精确小写
// "<think>"，部分模型输出变体时整个 think 块漏进正文，前端表现为
// 思考块拆分错位、markdown 渲染整段转义失效
var (
	thinkOpenRe  = regexp.MustCompile(`(?i)<think(\s[^>]*)?>`)
	thinkCloseRe = regexp.MustCompile(`(?i)</think\s*>`)
)

// ExtractThinkBlocks 从正文中提取所有 <think>...</think> 片段并剔除标签
func ExtractThinkBlocks(content string) (string, []string) {
	var answerParts []string
	var thinkParts []string
	rest := content

	for {
		loc := thinkOpenRe.FindStringIndex(rest)
		if loc == nil {
			answerParts = append(answerParts, rest)
			break
		}
		start := loc[0]
		answerParts = append(answerParts, rest[:start])
		tail := rest[loc[1]:]
		endLoc := thinkCloseRe.FindStringIndex(tail)
		if endLoc == nil {
			thinkParts = append(thinkParts, strings.TrimSpace(tail))
			break
		}
		thinkParts = append(thinkParts, strings.TrimSpace(tail[:endLoc[0]]))
		rest = tail[endLoc[1]:]
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
