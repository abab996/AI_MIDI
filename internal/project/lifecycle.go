package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aimidi/internal/llm"
)

// LocateUserInHistory 由显示列表 index 定位 full_history 中同一条 user 消息的下标
func LocateUserInHistory(fullHistory, display []map[string]any, index int) (int, error) {
	if index < 0 || index >= len(display) {
		return 0, fmt.Errorf("消息位置无效")
	}

	ordinal := 0
	for i := 0; i <= index; i++ {
		if r, ok := display[i]["role"].(string); ok && r == "user" {
			ordinal++
		}
	}

	var userPositions []int
	for i, m := range fullHistory {
		if r, ok := m["role"].(string); ok && r == "user" {
			userPositions = append(userPositions, i)
		}
	}

	if ordinal-1 < 0 || ordinal-1 >= len(userPositions) {
		return 0, fmt.Errorf("消息位置无效")
	}

	return userPositions[ordinal-1], nil
}

// normalizeAskQuestions 清洗 ask_user_question 参数中的问题列表。
// 与 chat.NormalizeQuestions 语义一致，但本包不可反向依赖 chat（会成环），
// 故按显示卡片所需的最小规则就地实现：去空白、选项 ≥2、最多 4 题。
func normalizeAskQuestions(raw any) []map[string]any {
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
		h, _ := m["header"].(string)
		q = strings.TrimSpace(q)
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

// RebuildDisplayFromHistory 从完整 API 历史（含 tool 调用）重建聊天显示列表。
// ask_user_question 调用重建为提问卡片（而非工具日志块），已回答/跳过的
// 卡片携带 result_text 兜底展示完整回答内容；pending 卡片（无工具结果，
// 见于应用重启后待回答的提问）由 loadTaskState 回填真实 question_id。
func RebuildDisplayFromHistory(allMessages []map[string]any) []map[string]any {
	toolResults := make(map[string]string)
	for _, m := range allMessages {
		if r, ok := m["role"].(string); ok && r == "tool" {
			tcid, _ := m["tool_call_id"].(string)
			if tcid != "" {
				content, _ := m["content"].(string)
				toolResults[tcid] = content
			}
		}
	}

	var display []map[string]any

	for _, m := range allMessages {
		role, _ := m["role"].(string)
		if role == "system" || role == "tool" {
			continue
		}

		if role == "user" {
			content, _ := m["content"].(string)
			display = append(display, map[string]any{
				"role":    "user",
				"content": content,
			})
			continue
		}

		if role == "assistant" {
			// 工具调用块
			var toolParts []string
			if toolCalls, ok := m["tool_calls"].([]any); ok {
				for _, tcRaw := range toolCalls {
					tcMap, ok := tcRaw.(map[string]any)
					if !ok {
						continue
					}
					tcid, _ := tcMap["id"].(string)
					fnMap, _ := tcMap["function"].(map[string]any)
					tcName, _ := fnMap["name"].(string)
					tcArgs, _ := fnMap["arguments"].(string)

					resultText := toolResults[tcid]
					if resultText == "" {
						resultText = "（该工具调用未完成）"
					}

					var parsedArgs map[string]any
					_ = json.Unmarshal([]byte(tcArgs), &parsedArgs)

					// 提问调用 → 提问卡片（展开/折叠、状态、回答摘要），
					// 不再退化为通用工具日志块
					if tcName == "ask_user_question" {
						questions := normalizeAskQuestions(parsedArgs["questions"])
						if len(questions) > 0 {
							status := "pending"
							if res, ok := toolResults[tcid]; ok {
								status = "answered"
								if strings.Contains(res, "选择跳过") {
									status = "skipped"
								}
							}
							card := map[string]any{
								"role":        "assistant",
								"type":        "question",
								"question_id": tcid, // 展示用占位 id；pending 卡由 loadTaskState 回填真实 id
								"questions":   questions,
								"status":      status,
							}
							if res, ok := toolResults[tcid]; ok && status == "answered" {
								card["result_text"] = res
							}
							display = append(display, card)
							continue
						}
					}

					// 统一格式化（含完整参数展开区，与 Python 一致）
					toolParts = append(toolParts, llm.FormatSingleToolEntry(tcName, parsedArgs, resultText))
				}
			}

			content, _ := m["content"].(string)
			reasoning, _ := m["reasoning_content"].(string)
			renderedContent := llm.FormatDisplayMessage(reasoning, content)

			var combined strings.Builder
			for _, tp := range toolParts {
				combined.WriteString(tp)
				combined.WriteString("\n\n")
			}
			combined.WriteString(renderedContent)

			if combined.Len() == 0 && len(toolParts) == 0 {
				continue // 纯提问轮：内容已由上方卡片承载，不再产出空气泡
			}

			display = append(display, map[string]any{
				"role":    "assistant",
				"content": combined.String(),
			})
		}
	}

	return display
}

// ExtractAICreatedEntries 提取该消息之后由 AI 工具创建或删除的文件与目录
func ExtractAICreatedEntries(fullHistory []map[string]any, fromHistIdx int) map[string][]string {
	files := make([]string, 0)
	dirs := make([]string, 0)
	deleted := make([]string, 0)

	for i := fromHistIdx + 1; i < len(fullHistory); i++ {
		m := fullHistory[i]
		if r, ok := m["role"].(string); !ok || r != "assistant" {
			continue
		}

		if toolCalls, ok := m["tool_calls"].([]any); ok {
			for _, tcRaw := range toolCalls {
				tc, ok := tcRaw.(map[string]any)
				if !ok {
					continue
				}
				fnMap, _ := tc["function"].(map[string]any)
				fnName, _ := fnMap["name"].(string)
				argsStr, _ := fnMap["arguments"].(string)

				var args map[string]any
				_ = json.Unmarshal([]byte(argsStr), &args)

				switch fnName {
				case "create_midi", "create_file", "write_file":
					fn, _ := args["filename"].(string)
					if fn == "" {
						fn, _ = args["path"].(string)
					}
					fn = strings.Trim(strings.TrimSpace(fn), "/")
					if fn != "" {
						files = append(files, fn)
					}
				case "create_folder":
					dn, _ := args["name"].(string)
					if dn == "" {
						dn, _ = args["path"].(string)
					}
					dn = strings.Trim(strings.TrimSpace(dn), "/")
					if dn != "" {
						dirs = append(dirs, dn)
					}
				case "delete_midi", "delete_file":
					fn, _ := args["filename"].(string)
					if fn == "" {
						fn, _ = args["path"].(string)
					}
					fn = strings.Trim(strings.TrimSpace(fn), "/")
					if fn != "" {
						deleted = append(deleted, fn)
					}
				}
			}
		}
	}

	return map[string][]string{
		"files":   files,
		"dirs":    dirs,
		"deleted": deleted,
	}
}

// RemoveAICreatedFiles 撤回 AI 创建的文件（移入回收站）与空文件夹
func RemoveAICreatedFiles(projectID string, currentFiles []MidiFileInfo, entries map[string][]string) ([]MidiFileInfo, map[string]any) {
	var trashRels []string
	var removedDirs []string
	var kept []MidiFileInfo
	targetFiles := make(map[string]bool)
	for _, f := range entries["files"] {
		targetFiles[filepath.ToSlash(f)] = true
	}

	for _, f := range currentFiles {
		cleanName := filepath.ToSlash(f.Name)
		baseName := filepath.Base(f.Name)
		if targetFiles[cleanName] || targetFiles[baseName] {
			rel := MoveToTrash(projectID, f)
			if rel != "" {
				trashRels = append(trashRels, rel)
				continue
			}
		}
		kept = append(kept, f)
	}

	baseDir := GetMidiBaseDir(projectID)
	for _, rel := range entries["dirs"] {
		target := filepath.Join(baseDir, filepath.FromSlash(rel))
		if entries, err := os.ReadDir(target); err == nil && len(entries) == 0 {
			_ = os.Remove(target)
			removedDirs = append(removedDirs, rel)
		}
	}

	SaveMidiManifest(projectID, kept)

	return kept, map[string]any{
		"trash_rels":    trashRels,
		"removed_dirs":  removedDirs,
		"removed_count": len(trashRels),
		"deleted_rels":  entries["deleted"],
	}
}

// RestoreAICreatedFiles 放弃撤回修改时恢复 AI 创建的文件（从回收站移回）与目录，
// 返回恢复后的文件清单。rollback 为 RemoveAICreatedFiles 返回的信息。
func RestoreAICreatedFiles(projectID string, rollback map[string]any) []MidiFileInfo {
	var trashRels []string
	if raw, ok := rollback["trash_rels"].([]string); ok {
		trashRels = raw
	} else if rawAny, ok := rollback["trash_rels"].([]any); ok {
		for _, item := range rawAny {
			if s, ok := item.(string); ok && s != "" {
				trashRels = append(trashRels, s)
			}
		}
	}
	if len(trashRels) > 0 {
		RestoreFromTrash(projectID, trashRels)
	}

	baseDir := GetMidiBaseDir(projectID)
	var removedDirs []string
	if raw, ok := rollback["removed_dirs"].([]string); ok {
		removedDirs = raw
	} else if rawAny, ok := rollback["removed_dirs"].([]any); ok {
		for _, item := range rawAny {
			if s, ok := item.(string); ok && s != "" {
				removedDirs = append(removedDirs, s)
			}
		}
	}
	for _, rel := range removedDirs {
		if rel == "" {
			continue
		}
		_ = os.MkdirAll(filepath.Join(baseDir, filepath.FromSlash(rel)), 0755)
	}

	if origFiles, ok := rollback["original_files"].([]MidiFileInfo); ok && origFiles != nil {
		SaveMidiManifest(projectID, origFiles)
		return origFiles
	} else if origAny, ok := rollback["original_files"].([]any); ok && len(origAny) > 0 {
		var origList []MidiFileInfo
		if b, err := json.Marshal(origAny); err == nil {
			if err := json.Unmarshal(b, &origList); err == nil && len(origList) > 0 {
				SaveMidiManifest(projectID, origList)
				return origList
			}
		}
	}

	// 重新扫描目录生成完整清单并持久化
	manifest := ScanMidiFilesKeepNotes(baseDir, nil)
	SaveMidiManifest(projectID, manifest)
	return manifest
}

// FindEditHistory 查找该消息（full_history 下标 histIdx）是否有编辑历史快照。
//
// 与 Python _find_edit_history 一致：按 hist_idx 匹配（新格式，下标稳定）；
// 旧格式记录（无 hist_idx）回退按 index 匹配。返回是否有匹配的编辑历史。
func FindEditHistory(editHistory []map[string]any, histIdx int) bool {
	if histIdx < 0 || len(editHistory) == 0 {
		return false
	}
	// 新格式：按 hist_idx 匹配
	for i := len(editHistory) - 1; i >= 0; i-- {
		h := editHistory[i]
		if hi, ok := h["hist_idx"]; ok {
			if hiF, ok2 := hi.(float64); ok2 && int(hiF) == histIdx {
				return true
			}
			if hiI, ok2 := hi.(int); ok2 && hiI == histIdx {
				return true
			}
		}
	}
	// 旧格式（无 hist_idx）：不按 index 兜底——新格式记录只按 hist_idx 匹配，
	// 避免 index 漂移后误匹配（与 Python 一致）
	return false
}
