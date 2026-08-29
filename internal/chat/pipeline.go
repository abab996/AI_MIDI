package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aimidi/internal/config"
	"aimidi/internal/llm"
	"aimidi/internal/mcp"
	"aimidi/internal/midi"
	"aimidi/internal/project"
	"aimidi/internal/tasks"
)

// StreamCallback 流式事件推送回调函数
type StreamCallback func(event map[string]any) error

// parseToolArgs 解析工具调用参数。模型可能输出畸形 JSON、空串或字面量
// "null"，任一情况都返回非 nil 空 map，保证调用方写入键（如 bpm 兜底）
// 不会触发 assignment to entry in nil map panic（该 panic 在桌面事件桥
// goroutine 里无法被 recover 拦截，会直接杀死整个应用）
func parseToolArgs(argsStr string) map[string]any {
	args := map[string]any{}
	if strings.TrimSpace(argsStr) == "" {
		return args
	}
	_ = json.Unmarshal([]byte(argsStr), &args)
	if args == nil {
		return map[string]any{}
	}
	return args
}

// ChatStream 多轮对话流式执行引擎
func ChatStream(ctx context.Context, projectID, message string, edit bool, resume bool, taskID *string, onEvent StreamCallback) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !resume && strings.TrimSpace(message) == "" {
		_ = onEvent(map[string]any{"type": "error", "message": "消息不能为空"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	settings := config.LoadSettings()
	if settings.APIKey == "" {
		_ = onEvent(map[string]any{"type": "error", "message": "⚠ 请先在设置页填写并保存 API Key。"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	taskRec := tasks.TaskEnsure(projectID, taskID, message)
	tid := taskRec.ID
	tasks.TaskMarkRunning(tid)

	// 任务停止信号 → ctx：用户点停止时立即取消在途 LLM 请求（而非只靠
	// 两个 chunk 之间的轮询——上游停滞时 ReadString 会无限阻塞，项目锁
	// 被持有导致该项目的后续对话永久挂起）。
	// 基座用 WithoutCancel：前端断开 SSE/切换视图 = 取消**订阅**而非取消
	// 任务——任务转后台续跑（openProject 的既定设计），轮次正常完成并
	// 落盘，重进可见完整回复。若用 req.Context() 直接传播，断开即中止
	// 生成：半截回复丢失，且下方 defer TaskFinish 把任务强标"已完成"
	taskCtx, cancelTask := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelTask()
	if ch := tasks.TaskCancelChan(tid); ch != nil {
		go func() {
			select {
			case <-ch:
				cancelTask()
			case <-taskCtx.Done():
			}
		}()
	}

	lock := GetProjectChatLock(projectID)
	lock.Lock()
	defer lock.Unlock()

	if tasks.TaskIsCancelled(tid) {
		tasks.TaskFinish(tid)
		_ = onEvent(map[string]any{"type": "error", "message": "任务已停止"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	s := GetSession(projectID)
	s.SwitchTask(tid)
	st := s.GetTaskState(tid)
	// 项目全局 BPM：AI 生成新 MIDI 的默认速度（已存在文件不改写）
	globalBPM := project.GetProjectBPM(projectID)
	// 会话状态锁：ChatDisplay/MidiFiles 的所有读写（含 HTTP 处理器里的
	// 读路径）都必须经过它，否则流式写入与 GET 载荷快照并发读写 map
	// 会触发 Go runtime fatal（recover 无法拦截，整个应用崩溃）
	fl := GetSessionLock(projectID)

	// 若用户未回答提问便发新消息：自动标记跳过
	if !resume && st.PendingQuestion != nil {
		fl.Lock()
		SkipPendingQuestion(st, projectID, tid, s.MidiFiles, taskRec.Legacy)
		fl.Unlock()
	}

	// 绑定工作区同步
	if project.GetWorkspaceDir(projectID) != "" {
		fl.Lock()
		s.MidiFiles = project.SyncWorkspaceToProjects(projectID, s.MidiFiles)
		fl.Unlock()
	}

	// 处理用户消息
	if !resume {
		fl.Lock()
		st.UndoStack = append(st.UndoStack, UndoEntry{
			Files: s.MidiFiles,
		})
		if edit && st.PendingEdit != nil {
			// 修改模式：/messages/edit 已截断并把旧 user 消息留在末尾，
			// 这里原地替换为编辑后的文本（此前误做追加，出现连续重复的用户消息）
			if n := len(st.FullHistory); n > 0 && st.FullHistory[n-1]["role"] == "user" {
				st.FullHistory[n-1] = map[string]any{"role": "user", "content": message}
			} else {
				st.FullHistory = append(st.FullHistory, map[string]any{"role": "user", "content": message})
			}
			if n := len(st.ChatDisplay); n > 0 && st.ChatDisplay[n-1]["role"] == "user" {
				st.ChatDisplay[n-1] = map[string]any{"role": "user", "content": message}
			} else {
				st.ChatDisplay = append(st.ChatDisplay, map[string]any{"role": "user", "content": message})
			}
			st.PendingEdit = nil
		} else {
			st.PendingEdit = nil
			st.FullHistory = append(st.FullHistory, map[string]any{"role": "user", "content": message})
			st.ChatDisplay = append(st.ChatDisplay, map[string]any{"role": "user", "content": message})
		}
		fl.Unlock()
	}

	_ = onEvent(map[string]any{
		"type":     "chat",
		"messages": st.ChatDisplay,
	})

	defer func() {
		tasks.TaskFinish(tid)
		_ = onEvent(map[string]any{"type": "done"})
	}()

	isGemini := config.IsGeminiProvider(settings.BaseURL)

	buildApiMessages := func(history []map[string]any, midiFiles []project.MidiFileInfo) []llm.ChatCompletionMessage {
		var msgs []llm.ChatCompletionMessage
		msgs = append(msgs, llm.ChatCompletionMessage{
			Role:    "system",
			Content: BuildSystemPrompt(midiFiles, globalBPM),
		})

		for _, m := range history {
			r, _ := m["role"].(string)
			c, _ := m["content"].(string)
			tcid, _ := m["tool_call_id"].(string)
			name, _ := m["name"].(string)

			var tcs []llm.ToolCall
			if tcRaw, ok := m["tool_calls"].([]any); ok {
				for _, t := range tcRaw {
					tm, ok := t.(map[string]any)
					if !ok {
						continue
					}
					fnMap, _ := tm["function"].(map[string]any)
					id, _ := tm["id"].(string)
					var fnName, fnArgs string
					if fnMap != nil {
						fnName, _ = fnMap["name"].(string)
						fnArgs, _ = fnMap["arguments"].(string)
					}
					// 持久化历史反序列化后字段类型可能缺失/漂移，
					// 断言失败直接跳过该条，避免击杀整个 SSE 协程
					if id == "" || fnName == "" {
						continue
					}
					tcs = append(tcs, llm.ToolCall{
						ID:   id,
						Type: "function",
						Function: llm.FunctionCall{
							Name:      fnName,
							Arguments: fnArgs,
						},
					})
				}
			}

			msgs = append(msgs, llm.ChatCompletionMessage{
				Role:       r,
				Content:    c,
				ToolCallID: tcid,
				Name:       name,
				ToolCalls:  tcs,
			})
		}

		msgs = llm.EnsureToolClosure(msgs)
		msgs = llm.SanitizeMessages(msgs, isGemini)
		return msgs
	}

	// 多轮工具调用循环（最多 10 轮）
	for round := 0; round < config.MaxToolRounds; round++ {
		if tasks.TaskIsCancelled(tid) {
			break
		}

		fl.Lock()
		apiMessages := buildApiMessages(st.FullHistory, s.MidiFiles)

		// 检查是否需要压缩并立即重新构建 API 消息
		if ShouldCompact(apiMessages) {
			st.FullHistory = CompactFullHistory(st.FullHistory)
			apiMessages = buildApiMessages(st.FullHistory, s.MidiFiles)
		}
		fl.Unlock()

		var tools []llm.ToolDefinition
		tools = append(tools, mcp.GetMCPTools(projectID)...)
		tools = append(tools, mcp.GetLocalTools()...)

		// 发起流式请求
		streamResp, err := streamChatCompletion(taskCtx, settings, apiMessages, tools, isGemini)
		if err != nil {
			slog.Error("调用 AI 流式接口失败", "err", err)
			if taskCtx.Err() == nil {
				_ = onEvent(map[string]any{"type": "error", "message": fmt.Sprintf("AI 调用失败: %v", err)})
			} else {
				_ = onEvent(map[string]any{"type": "error", "message": "任务已停止"})
				_ = onEvent(map[string]any{"type": "done"})
			}
			// 用户消息与上下文已入内存：落盘，避免重启后从历史中消失
			fl.Lock()
			project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
			fl.Unlock()
			break
		}

		// 消费流式响应
		deltaContent, deltaReasoning, toolCalls, err := processStreamChunks(streamResp, st, fl, onEvent, tid)
		_ = streamResp.Body.Close()
		cancelled := tasks.TaskIsCancelled(tid) || taskCtx.Err() != nil
		if err != nil {
			slog.Error("处理流式响应失败", "err", err)
		}
		if err != nil || cancelled {
			// 停止/断流：保留已生成的部分内容并落盘（停止按钮的语义就是
			// "终止并保留已输出的内容"；断流同理，避免已生成内容丢失），
			// 丢弃半截工具调用（参数 JSON 可能被截断，执行会产生意外副作用）
			fl.Lock()
			if deltaContent != "" || deltaReasoning != "" {
				st.FullHistory = append(st.FullHistory, map[string]any{
					"role":              "assistant",
					"content":           deltaContent,
					"reasoning_content": deltaReasoning,
				})
			}
			project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
			fl.Unlock()
			if cancelled {
				_ = onEvent(map[string]any{"type": "error", "message": "任务已停止"})
			} else if err != nil {
				_ = onEvent(map[string]any{"type": "error", "message": fmt.Sprintf("AI 调用失败: %v", err)})
			}
			_ = onEvent(map[string]any{"type": "done"})
			break
		}

		if len(toolCalls) == 0 {
			// 无工具调用：回合结束
			fl.Lock()
			formatted := llm.FormatDisplayMessage(deltaReasoning, deltaContent)
			assistantMsg := map[string]any{
				"role":              "assistant",
				"content":           deltaContent,
				"reasoning_content": deltaReasoning,
			}
			st.FullHistory = append(st.FullHistory, assistantMsg)
			// 提问卡片（role=assistant 且带 type=question）不可被空响应覆盖
			lastIdx := len(st.ChatDisplay) - 1
			if lastIdx >= 0 && st.ChatDisplay[lastIdx]["role"] == "assistant" && st.ChatDisplay[lastIdx]["type"] != "question" {
				st.ChatDisplay[lastIdx] = map[string]any{
					"role":    "assistant",
					"content": formatted,
				}
			} else {
				st.ChatDisplay = append(st.ChatDisplay, map[string]any{
					"role":    "assistant",
					"content": formatted,
				})
			}

			_ = onEvent(map[string]any{
				"type":     "chat",
				"messages": st.ChatDisplay,
			})

			project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
			fl.Unlock()
			break
		}

		// 处理工具调用
		var tcHistoryList []any
		for _, tc := range toolCalls {
			tcHistoryList = append(tcHistoryList, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				},
				"extra_content": tc.ExtraContent,
			})
		}

		fl.Lock()
		st.FullHistory = append(st.FullHistory, map[string]any{
			"role":              "assistant",
			"content":           deltaContent,
			"reasoning_content": deltaReasoning,
			"tool_calls":        tcHistoryList,
		})
		fl.Unlock()

		pausedByQuestion := false
		baseDir := project.GetMidiBaseDir(projectID)
		mirrorDir := project.GetMidiMirrorDir(projectID)

		for i, tc := range toolCalls {
			// 停止信号：跳过剩余工具执行
			if tasks.TaskIsCancelled(tid) || taskCtx.Err() != nil {
				break
			}
			fnName := tc.Function.Name
			rawArgs := parseToolArgs(tc.Function.Arguments)

			// create_midi 未带 bpm 时兜底注入全局 BPM（提示词已要求 AI
			// 使用全局值，这里保证漏参时生成结果仍然一致）
			if fnName == "create_midi" && rawArgs["bpm"] == nil {
				rawArgs["bpm"] = globalBPM
			}

			if fnName == "ask_user_question" {
				questions := NormalizeQuestions(rawArgs["questions"])
				if len(questions) == 0 {
					errText := "错误：ask_user_question 参数无效（缺少 questions 或选项不足）"
					entry := llm.FormatSingleToolEntry(fnName, rawArgs, errText)
					fl.Lock()
					st.FullHistory = append(st.FullHistory, map[string]any{
						"role":         "tool",
						"tool_call_id": tc.ID,
						"name":         fnName,
						"content":      errText,
					})
					st.ChatDisplay = append(st.ChatDisplay, map[string]any{
						"role":    "assistant",
						"content": entry,
					})
					_ = onEvent(map[string]any{
						"type":     "chat",
						"messages": st.ChatDisplay,
					})
					fl.Unlock()
					continue
				}

				// 同轮排在提问之后的调用 stash 待回答后补执行；
				// 但后续的 ask_user_question 不 stash——补执行通道
				// （AnswerStream）直接走 ExecuteTool 会返回「未知工具」，
				// 这里立即以工具结果回填，引导模型下一轮再问
				var extraAsks []llm.ToolCall
				var remCalls []map[string]any
				for j := i + 1; j < len(toolCalls); j++ {
					if toolCalls[j].Function.Name == "ask_user_question" {
						extraAsks = append(extraAsks, toolCalls[j])
						continue
					}
					remCalls = append(remCalls, map[string]any{
						"id":   toolCalls[j].ID,
						"type": "function",
						"function": map[string]any{
							"name":      toolCalls[j].Function.Name,
							"arguments": toolCalls[j].Function.Arguments,
						},
					})
				}

				qid := SetPendingQuestion(projectID, tid, tc.ID, questions, remCalls)
				fl.Lock()
				st.ChatDisplay = append(st.ChatDisplay, map[string]any{
					"role":        "assistant",
					"type":        "question",
					"question_id": qid,
					"questions":   questions,
					"status":      "pending",
				})

				for _, extra := range extraAsks {
					note := "本轮已有一个提问正在等待用户回答，请等待其结果后再提问"
					st.FullHistory = append(st.FullHistory, map[string]any{
						"role":         "tool",
						"tool_call_id": extra.ID,
						"name":         extra.Function.Name,
						"content":      note,
					})
					var extraArgs map[string]any
					_ = json.Unmarshal([]byte(extra.Function.Arguments), &extraArgs)
					st.ChatDisplay = append(st.ChatDisplay, map[string]any{
						"role":    "assistant",
						"content": llm.FormatSingleToolEntry(extra.Function.Name, extraArgs, note),
					})
				}

				_ = onEvent(map[string]any{
					"type":     "chat",
					"messages": st.ChatDisplay,
				})

				project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
				fl.Unlock()
				pausedByQuestion = true
				break
			}

			// 工具「执行中」占位块：用户第一时间看到工具标签 + ⏳ 进度标记，
			// 完成后由完成块整体替换。
			pendingEntry := llm.FormatPendingToolEntry(fnName, rawArgs)
			fl.Lock()
			st.ChatDisplay = append(st.ChatDisplay, map[string]any{
				"role":    "assistant",
				"content": pendingEntry,
			})
			_ = onEvent(map[string]any{
				"type":     "chat",
				"messages": st.ChatDisplay,
			})
			fl.Unlock()

			// 执行 MCP 工具
			resText, _ := mcp.ExecuteTool(fnName, rawArgs, baseDir, mirrorDir)

			if fnName == "create_midi" {
				fn, _ := rawArgs["filename"].(string)
				if fn == "" {
					fn = "output.mid"
				}
				targetPath, err := mcp.SafeJoin(baseDir, fn)
				if err == nil {
					rel, _ := filepath.Rel(baseDir, targetPath)
					fi, _ := os.Stat(targetPath)
					size := int64(0)
					if fi != nil {
						size = fi.Size()
					}
					noteList, _ := midi.GetNote(targetPath, false)
					newInfo := project.MidiFileInfo{
						Name:      filepath.ToSlash(rel),
						Path:      targetPath,
						Size:      size,
						NoteTable: strings.Join(noteList, "\n"),
					}

					fl.Lock()
					var updated []project.MidiFileInfo
					for _, f := range s.MidiFiles {
						if f.Name != newInfo.Name && f.Path != newInfo.Path {
							updated = append(updated, f)
						}
					}
					updated = append(updated, newInfo)
					s.MidiFiles = updated

					_ = onEvent(map[string]any{
						"type":  "files",
						"files": s.MidiFiles,
					})
					fl.Unlock()

					relToProj, _ := filepath.Rel(config.ProjectRoot, targetPath)
					_ = onEvent(map[string]any{
						"type": "download",
						"url":  "/api/files/download?path=" + url.QueryEscape(filepath.ToSlash(relToProj)),
					})
				}
			} else if fnName == "delete_midi" {
				fn, _ := rawArgs["filename"].(string)
				cleanFn := filepath.ToSlash(filepath.Clean(fn))
				fl.Lock()
				var updated []project.MidiFileInfo
				for _, f := range s.MidiFiles {
					fClean := filepath.ToSlash(filepath.Clean(f.Name))
					if strings.Contains(cleanFn, "/") {
						if fClean != cleanFn && f.Name != fn {
							updated = append(updated, f)
						}
					} else {
						if fClean != cleanFn && f.Name != fn && filepath.Base(f.Name) != fn {
							updated = append(updated, f)
						}
					}
				}
				s.MidiFiles = updated
				_ = onEvent(map[string]any{
					"type":  "files",
					"files": s.MidiFiles,
				})
				fl.Unlock()
			}

			fl.Lock()
			st.FullHistory = append(st.FullHistory, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"name":         fnName,
				"content":      resText,
			})

			// 工具完成块替换占位块（summary 一致，正文换为结果）
			completedEntry := llm.FormatSingleToolEntry(fnName, rawArgs, resText)
			if len(st.ChatDisplay) > 0 && st.ChatDisplay[len(st.ChatDisplay)-1]["content"] == pendingEntry {
				st.ChatDisplay[len(st.ChatDisplay)-1]["content"] = completedEntry
			} else {
				st.ChatDisplay = append(st.ChatDisplay, map[string]any{
					"role":    "assistant",
					"content": completedEntry,
				})
			}
			_ = onEvent(map[string]any{
				"type":     "chat",
				"messages": st.ChatDisplay,
			})
			fl.Unlock()
		}

		fl.Lock()
		project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
		fl.Unlock()

		if pausedByQuestion {
			break
		}
	}

	return nil
}

// AnswerStream 用户回答提问后恢复对话流
func AnswerStream(ctx context.Context, projectID, questionID string, answers any, onEvent StreamCallback) error {
	taskRec := tasks.TaskResumeByQuestion(projectID, questionID)
	if taskRec == nil {
		_ = onEvent(map[string]any{"type": "error", "message": "未找到对应的待回答任务"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	// 与 ChatStream 相同的项目对话锁：并发的 /api/chat 不得与回答的
	// 补执行/状态修改交错（此前本函数全程无锁，存在并发 map 读写风险）。
	// 锁只覆盖预处理段——末尾重入 ChatStream（其内部自行加锁）前必须释放。
	lock := GetProjectChatLock(projectID)
	lock.Lock()

	s := GetSession(projectID)
	tid := taskRec.ID
	s.SwitchTask(tid)
	st := s.GetTaskState(tid)
	fl := GetSessionLock(projectID)
	globalBPM := project.GetProjectBPM(projectID)

	if st.PendingQuestion == nil {
		lock.Unlock()
		_ = onEvent(map[string]any{"type": "error", "message": "该提问已失效或已被回答"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	fl.Lock()
	pending := st.PendingQuestion
	st.PendingQuestion = nil

	// 记录撤销快照（回答前状态）
	st.UndoStack = append(st.UndoStack, UndoEntry{
		Files: s.MidiFiles,
	})

	questions := NormalizeQuestions(pending["questions"])
	tcID, _ := pending["tool_call_id"].(string)

	var remCalls []map[string]any
	if rcList, ok := pending["remaining_calls"].([]any); ok {
		for _, rc := range rcList {
			if rcMap, ok := rc.(map[string]any); ok {
				remCalls = append(remCalls, rcMap)
			}
		}
	} else if rcList, ok := pending["remaining_calls"].([]map[string]any); ok {
		remCalls = rcList
	}

	ansText := FormatAnswerResult(questions, answers)
	st.FullHistory = append(st.FullHistory, map[string]any{
		"role":         "tool",
		"tool_call_id": tcID,
		"name":         "ask_user_question",
		"content":      ansText,
	})

	MarkQuestionBlock(st.ChatDisplay, questionID, answers)
	fl.Unlock()

	// 补执行排在提问之后的工具调用
	baseDir := project.GetMidiBaseDir(projectID)
	mirrorDir := project.GetMidiMirrorDir(projectID)
	for _, call := range remCalls {
		fnMap, _ := call["function"].(map[string]any)
		fnName, _ := fnMap["name"].(string)
		argsStr, _ := fnMap["arguments"].(string)
		args := parseToolArgs(argsStr)

		// 与主循环一致：create_midi 缺 bpm 时兜底注入全局 BPM
		if fnName == "create_midi" && args["bpm"] == nil {
			args["bpm"] = globalBPM
		}

		// 占位 + 执行 + 完成块（与主循环一致，用户能看到补执行的即时反馈）
		pendingEntry := llm.FormatPendingToolEntry(fnName, args)
		fl.Lock()
		st.ChatDisplay = append(st.ChatDisplay, map[string]any{
			"role":    "assistant",
			"content": pendingEntry,
		})
		_ = onEvent(map[string]any{
			"type":     "chat",
			"messages": st.ChatDisplay,
		})
		fl.Unlock()

		resText, _ := mcp.ExecuteTool(fnName, args, baseDir, mirrorDir)

		// create_midi 补执行也要推送 files/download 事件
		if fnName == "create_midi" {
			fn, _ := args["filename"].(string)
			if fn == "" {
				fn = "output.mid"
			}
			if targetPath, err := mcp.SafeJoin(baseDir, fn); err == nil {
				rel, _ := filepath.Rel(baseDir, targetPath)
				fi, _ := os.Stat(targetPath)
				size := int64(0)
				if fi != nil {
					size = fi.Size()
				}
				noteList, _ := midi.GetNote(targetPath, false)
				newInfo := project.MidiFileInfo{
					Name:      filepath.ToSlash(rel),
					Path:      targetPath,
					Size:      size,
					NoteTable: strings.Join(noteList, "\n"),
				}
				fl.Lock()
				var updated []project.MidiFileInfo
				for _, f := range s.MidiFiles {
					if f.Name != newInfo.Name && f.Path != newInfo.Path {
						updated = append(updated, f)
					}
				}
				updated = append(updated, newInfo)
				s.MidiFiles = updated
				_ = onEvent(map[string]any{"type": "files", "files": s.MidiFiles})
				fl.Unlock()
				relToProj, _ := filepath.Rel(config.ProjectRoot, targetPath)
				_ = onEvent(map[string]any{
					"type": "download",
					"url":  "/api/files/download?path=" + url.QueryEscape(filepath.ToSlash(relToProj)),
				})
			}
		}

		fl.Lock()
		st.FullHistory = append(st.FullHistory, map[string]any{
			"role":         "tool",
			"tool_call_id": call["id"],
			"name":         fnName,
			"content":      resText,
		})

		completedEntry := llm.FormatSingleToolEntry(fnName, args, resText)
		if len(st.ChatDisplay) > 0 && st.ChatDisplay[len(st.ChatDisplay)-1]["content"] == pendingEntry {
			st.ChatDisplay[len(st.ChatDisplay)-1]["content"] = completedEntry
		} else {
			st.ChatDisplay = append(st.ChatDisplay, map[string]any{
				"role":    "assistant",
				"content": completedEntry,
			})
		}
		fl.Unlock()
	}
	fl.Lock()
	_ = onEvent(map[string]any{
		"type":     "chat",
		"messages": st.ChatDisplay,
	})

	project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
	fl.Unlock()

	lock.Unlock()

	// resume 模式续跑（ChatStream 内部自行获取项目对话锁）
	return ChatStream(ctx, projectID, "", false, true, &tid, onEvent)
}

func streamChatCompletion(ctx context.Context, s config.Settings, messages []llm.ChatCompletionMessage, tools []llm.ToolDefinition, isGemini bool) (*http.Response, error) {
	validatedURL, err := config.ValidateBaseURL(s.BaseURL, s.APIPath)
	if err != nil {
		return nil, err
	}

	model := s.Model
	if model == "" {
		model = config.DefaultModel
	}

	reqBody := llm.ChatCompletionRequest{
		Model:               model,
		Messages:            messages,
		Tools:               tools,
		Stream:              true,
		ReasoningEffort:     s.ReasoningEffort,
		MaxTokens:           s.MaxTokens,
		MaxCompletionTokens: s.MaxCompletionTokens,
	}

	if s.ThinkingEnabled && isGemini {
		reqBody.ExtraBody = map[string]any{
			"thinking": map[string]string{"type": "enabled"},
		}
	}

	endpoint := llm.JoinEndpoint(validatedURL, "/v1/chat/completions")
	client := llm.NewStreamHTTPClient(60 * time.Second)

	strippableSteps := []string{"extra_body", "reasoning_effort", "max_tokens", "max_completion_tokens"}
	stepIdx := 0

	for attempt := 0; attempt < len(strippableSteps)+1; attempt++ {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+s.APIKey)

		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		if resp.StatusCode == http.StatusBadRequest && stepIdx < len(strippableSteps) {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			param := strippableSteps[stepIdx]
			stepIdx++
			slog.Warn("流式 API 返回 400，剥除参数后重试", "param", param, "detail", string(respBody))
			switch param {
			case "extra_body":
				reqBody.ExtraBody = nil
			case "reasoning_effort":
				reqBody.ReasoningEffort = ""
			case "max_tokens":
				reqBody.MaxTokens = nil
			case "max_completion_tokens":
				reqBody.MaxCompletionTokens = nil
			}
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("API 请求失败 (%d): %s", resp.StatusCode, string(respBody))
	}

	return nil, fmt.Errorf("重试次数耗尽，未能完成流式请求")
}

func processStreamChunks(resp *http.Response, st *TaskState, fl *sync.RWMutex, onEvent StreamCallback, tid string) (string, string, []llm.ToolCall, error) {
	reader := bufio.NewReader(resp.Body)
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	toolCallMap := make(map[int]*llm.ToolCall)

	// 占位 assistant 消息
	fl.Lock()
	st.ChatDisplay = append(st.ChatDisplay, map[string]any{
		"role":    "assistant",
		"content": "",
	})
	fl.Unlock()

	lastEmit := time.Now()
	sentReasoning, sentContent := 0, 0
	var streamErr error

	for {
		if tasks.TaskIsCancelled(tid) {
			break
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				// 上游断流/连接中断（含用户停止导致的 ctx 取消）：
				// 与正常结束一样走统一收尾——已生成的部分内容保留展示并落盘
				streamErr = err
			}
			break
		}

		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var chunk llm.ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta
		reasoning, text := llm.SplitContentBlocks(delta.Content)
		if delta.ReasoningContent != "" {
			reasoning += delta.ReasoningContent
		}

		contentBuilder.WriteString(text)
		reasoningBuilder.WriteString(reasoning)

		for _, tc := range delta.ToolCalls {
			existing, ok := toolCallMap[tc.Index]
			if !ok {
				existing = &llm.ToolCall{
					Index: tc.Index,
					ID:    tc.ID,
					Type:  "function",
					Function: llm.FunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
					ExtraContent: tc.ExtraContent,
				}
				toolCallMap[tc.Index] = existing
			} else {
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Function.Name != "" {
					existing.Function.Name += tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					existing.Function.Arguments += tc.Function.Arguments
				}
				if tc.ExtraContent != nil {
					existing.ExtraContent = tc.ExtraContent
				}
			}
		}

		// 增量推送：只发新增的推理/正文片段（轻量、逐 chunk 级实时），
		// 替代旧版每 25ms 重传整个 ChatDisplay 数组（O(n²)，长回复越到
		// 后面越卡）。小窗口合并减少 IPC 帧率；msg_index 让前端区分回合。
		if time.Since(lastEmit) > 8*time.Millisecond {
			lastEmit = time.Now()
			r := reasoningBuilder.String()
			c := contentBuilder.String()
			rd, cd := r[sentReasoning:], c[sentContent:]
			if rd != "" || cd != "" {
				sentReasoning, sentContent = len(r), len(c)
				_ = onEvent(map[string]any{
					"type":            "chat_delta",
					"msg_index":       len(st.ChatDisplay) - 1,
					"reasoning_delta": rd,
					"content_delta":   cd,
				})
			}
		}
	}

	fl.Lock()
	if contentBuilder.Len() == 0 && reasoningBuilder.Len() == 0 {
		if len(st.ChatDisplay) > 0 && st.ChatDisplay[len(st.ChatDisplay)-1]["content"] == "" {
			st.ChatDisplay = st.ChatDisplay[:len(st.ChatDisplay)-1]
		}
	} else if len(st.ChatDisplay) > 0 {
		formatted := llm.FormatDisplayMessage(reasoningBuilder.String(), contentBuilder.String())
		st.ChatDisplay[len(st.ChatDisplay)-1]["content"] = formatted
		_ = onEvent(map[string]any{
			"type":     "chat",
			"messages": st.ChatDisplay,
		})
	}
	fl.Unlock()

	return contentBuilder.String(), reasoningBuilder.String(), mapToToolCallList(toolCallMap), streamErr
}

func mapToToolCallList(m map[int]*llm.ToolCall) []llm.ToolCall {
	var list []llm.ToolCall
	for i := 0; i < len(m); i++ {
		if tc, ok := m[i]; ok {
			list = append(list, *tc)
		}
	}
	return list
}
