package llm

// SanitizeMessages 清理并修复消息列表，确保兼容 Gemini / OpenAI 等 API 的严格要求
func SanitizeMessages(messages []ChatCompletionMessage, isGemini bool) []ChatCompletionMessage {
	tcIDToName := make(map[string]string)
	sanitized := make([]ChatCompletionMessage, 0, len(messages))

	for _, msg := range messages {
		msgCopy := msg

		if msgCopy.Role == "assistant" {
			// reasoning_content 仅用于前端展示与持久化，发送给 API 前剥除
			msgCopy.ReasoningContent = ""

			for i := range msgCopy.ToolCalls {
				tc := &msgCopy.ToolCalls[i]
				if tc.ID != "" && tc.Function.Name != "" {
					tcIDToName[tc.ID] = tc.Function.Name
				}

				if isGemini {
					// 兜底补上 Gemini 思考模型要求的 thought_signature
					if tc.ExtraContent == nil || tc.ExtraContent.Google == nil || tc.ExtraContent.Google["thought_signature"] == "" {
						tc.ExtraContent = &GoogleExtraBody{
							Google: map[string]string{
								"thought_signature": "skip_thought_signature_validator",
							},
						}
					}
				} else {
					tc.ExtraContent = nil
				}
			}
		} else if msgCopy.Role == "tool" {
			if msgCopy.Name == "" && msgCopy.ToolCallID != "" {
				if name, ok := tcIDToName[msgCopy.ToolCallID]; ok {
					msgCopy.Name = name
				}
			}
		}

		sanitized = append(sanitized, msgCopy)
	}

	return sanitized
}

// EnsureToolClosure 兜底修复悬挂的 tool_calls，保证 OpenAI tool-calling 协议闭合
func EnsureToolClosure(messages []ChatCompletionMessage) []ChatCompletionMessage {
	var result []ChatCompletionMessage
	pendingIDs := make(map[string]string) // tool_call_id -> tool_name

	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					pendingIDs[tc.ID] = tc.Function.Name
				}
			}
		} else if msg.Role == "tool" {
			if msg.ToolCallID != "" {
				delete(pendingIDs, msg.ToolCallID)
			}
		} else if msg.Role == "user" {
			// 用户消息之前必须闭合所有悬挂的 tool_calls
			for tcID, tcName := range pendingIDs {
				result = append(result, ChatCompletionMessage{
					Role:       "tool",
					ToolCallID: tcID,
					Content:    "该工具调用未完成（对话中断），请根据上下文重新执行或忽略。",
					Name:       tcName,
				})
			}
			pendingIDs = make(map[string]string)
		}
		result = append(result, msg)
	}

	// 历史末尾若存在未闭合的 tool_calls（如最后一条消息为 assistant 且调用被异常打断），强制补齐闭包
	for tcID, tcName := range pendingIDs {
		result = append(result, ChatCompletionMessage{
			Role:       "tool",
			ToolCallID: tcID,
			Content:    "该工具调用未完成（对话中断），请根据上下文重新执行或忽略。",
			Name:       tcName,
		})
	}

	return result
}
