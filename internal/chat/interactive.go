package chat

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"aimidi/internal/project"
	"aimidi/internal/tasks"
)

// NormalizeQuestions 清洗模型传入的问题参数
func NormalizeQuestions(raw any) []map[string]any {
	if raw == nil {
		return nil
	}

	var list []any
	switch v := raw.(type) {
	case []any:
		list = v
	case []map[string]any:
		for _, item := range v {
			list = append(list, item)
		}
	default:
		return nil
	}

	var normalized []map[string]any
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		q, _ := m["question"].(string)
		q = strings.TrimSpace(q)
		h, _ := m["header"].(string)
		h = strings.TrimSpace(h)

		var options []map[string]any
		switch opts := m["options"].(type) {
		case []any:
			for _, optItem := range opts {
				if optMap, ok := optItem.(map[string]any); ok {
					label, _ := optMap["label"].(string)
					label = strings.TrimSpace(label)
					if label == "" {
						continue
					}
					desc, _ := optMap["description"].(string)
					options = append(options, map[string]any{
						"label":       label,
						"description": strings.TrimSpace(desc),
					})
				}
			}
		case []map[string]any:
			for _, optMap := range opts {
				label, _ := optMap["label"].(string)
				label = strings.TrimSpace(label)
				if label == "" {
					continue
				}
				desc, _ := optMap["description"].(string)
				options = append(options, map[string]any{
					"label":       label,
					"description": strings.TrimSpace(desc),
				})
			}
		}

		if q == "" || h == "" || len(options) < 2 {
			continue
		}

		multiSelect, _ := m["multiSelect"].(bool)
		normalized = append(normalized, map[string]any{
			"question":    q,
			"header":      h,
			"options":     options,
			"multiSelect": multiSelect,
		})
	}

	if len(normalized) > 4 {
		normalized = normalized[:4]
	}
	return normalized
}

// FormatAnswerResult 将用户回答格式化为自然语言的 tool 结果文本
func FormatAnswerResult(questions []map[string]any, answers any) string {
	ansList, ok := answers.([]any)
	if !ok || len(ansList) == 0 {
		return "用户未回答提问（选择跳过），请基于现有信息按你的专业判断继续。"
	}

	answerMap := make(map[int]map[string]any)
	for _, a := range ansList {
		if m, ok := a.(map[string]any); ok {
			if idx, ok := m["question_index"].(float64); ok {
				answerMap[int(idx)] = m
			} else if idx, ok := m["question_index"].(int); ok {
				answerMap[idx] = m
			}
		}
	}

	var lines []string
	lines = append(lines, "用户对提问的回答：")

	for idx, q := range questions {
		qText, _ := q["question"].(string)
		ans, hasAns := answerMap[idx]
		lines = append(lines, fmt.Sprintf("%d. %s", idx+1, qText))
		if !hasAns {
			lines = append(lines, "   → 未回答")
			continue
		}

		selected, _ := ans["selected"].([]any)
		for _, s := range selected {
			label, _ := s.(string)
			desc := ""
			if opts, ok := q["options"].([]map[string]any); ok {
				for _, opt := range opts {
					if opt["label"] == label {
						desc, _ = opt["description"].(string)
						break
					}
				}
			} else if optsRaw, ok := q["options"].([]any); ok {
				for _, optItem := range optsRaw {
					if optMap, ok := optItem.(map[string]any); ok {
						if optMap["label"] == label {
							desc, _ = optMap["description"].(string)
							break
						}
					}
				}
			}
			if desc != "" {
				lines = append(lines, fmt.Sprintf("   → %s（%s）", label, desc))
			} else {
				lines = append(lines, fmt.Sprintf("   → %s", label))
			}
		}

		other, _ := ans["other"].(string)
		other = strings.TrimSpace(other)
		if other != "" {
			lines = append(lines, fmt.Sprintf("   → 自定义: %s", other))
		}
	}

	return strings.Join(lines, "\n")
}

// SetPendingQuestion 保存提问状态到会话并更新任务状态
func SetPendingQuestion(projectID, taskID, toolCallID string, questions []map[string]any, remainingCalls []map[string]any) string {
	qid := strings.ReplaceAll(uuid.New().String(), "-", "")
	s := GetSession(projectID)
	st := s.GetTaskState(taskID)

	pending := map[string]any{
		"question_id":     qid,
		"tool_call_id":    toolCallID,
		"questions":       questions,
		"remaining_calls": remainingCalls,
		"task_id":         taskID,
	}
	st.PendingQuestion = pending
	tasks.TaskMarkNeedsConfirmation(projectID, pending)

	return qid
}

// MarkQuestionBlock 将显示列表中的提问块标记为 answered 或 skipped
func MarkQuestionBlock(chatDisplay []map[string]any, questionID string, answers any) {
	for _, m := range chatDisplay {
		if t, _ := m["type"].(string); t == "question" {
			if qid, _ := m["question_id"].(string); qid == questionID {
				if answers != nil {
					m["status"] = "answered"
					m["answers"] = answers
				} else {
					m["status"] = "skipped"
					m["answers"] = []any{}
				}
				break
			}
		}
	}
}

// SkipPendingQuestion 处理用户未回答提问便发送新消息的场景
func SkipPendingQuestion(st *TaskState, projectID, taskID string, midiFiles []project.MidiFileInfo, legacy bool) {
	if st == nil || st.PendingQuestion == nil {
		return
	}

	pending := st.PendingQuestion
	st.PendingQuestion = nil

	tcID, _ := pending["tool_call_id"].(string)
	qid, _ := pending["question_id"].(string)

	st.FullHistory = append(st.FullHistory, map[string]any{
		"role":         "tool",
		"tool_call_id": tcID,
		"name":         "ask_user_question",
		"content":      "用户未回答提问（选择跳过），请基于现有信息按你的专业判断继续。",
	})

	MarkQuestionBlock(st.ChatDisplay, qid, nil)
	project.SaveHistory(projectID, st.FullHistory, midiFiles, taskID, legacy)
	tasks.TaskForceComplete(taskID)
}
