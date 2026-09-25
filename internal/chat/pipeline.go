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
	"sort"
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

	// 任务停止信号 → ctx：用户点停止时立即取消在途 LLM 请求（而非只靠
	// 两个 chunk 之间的轮询——上游停滞时 ReadString 会无限阻塞，项目锁
	// 被持有导致该项目的后续对话永久挂起）。
	// 基座用 WithoutCancel：前端断开 SSE/切换视图 = 取消**订阅**而非取消
	// 任务——任务转后台续跑（openProject 的既定设计），轮次正常完成并
	// 落盘，重进可见完整回复。若用 req.Context() 直接传播，断开即中止
	// 生成：半截回复丢失，且下方 defer TaskFinish 把任务强标"已完成"
	taskCtx, cancelTask := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelTask()

	if err := AcquireProjectChatLock(taskCtx, projectID); err != nil {
		// 等锁期间任务被停止（如持锁的上游卡死任务被用户点停）：不再无限
		// 排队，立即退出并如实上报，避免界面一直转圈
		tasks.TaskFinish(tid)
		_ = onEvent(map[string]any{"type": "error", "message": "任务已停止"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}
	defer ReleaseProjectChatLock(projectID)

	if tasks.TaskIsCancelled(tid) {
		tasks.TaskFinish(tid)
		_ = onEvent(map[string]any{"type": "error", "message": "任务已停止"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	// TaskMarkRunning 必须在拿到项目锁之后调用：它先 close 旧停止信号
	// chan 再建新的——若在等锁前调用，同任务上一条消息仍在生成时，本条
	// 消息会提前关闭旧 chan，把正在生成的流当成「卡死的旧轮」误杀。
	// 拿到锁意味着上一轮已正常释放，旧 watcher 必然已随旧 taskCtx 退出。
	// 等锁期间用户点停止：TaskStop 关闭的是上一轮的旧 chan（尚未被替换），
	// 旧轮被取消后锁释放，本轮在上方 TaskIsCancelled 处退出
	tasks.TaskMarkRunning(tid)
	if ch := tasks.TaskCancelChan(tid); ch != nil {
		go func() {
			select {
			case <-ch:
				cancelTask()
			case <-taskCtx.Done():
			}
		}()
	}

	s := GetSession(projectID)
	s.SwitchTask(tid)
	st := s.GetTaskState(tid)
	// 项目全局 BPM：AI 生成新 MIDI 的默认速度（已存在文件不改写）
	globalBPM := project.GetProjectBPM(projectID)
	// 会话状态锁说明：ChatDisplay/MidiFiles 的所有读写（含 HTTP 处理器里
	// 的读路径）都必须经过 GetSessionLock(projectID)，否则流式写入与 GET
	// 载荷快照并发读写 map 会触发 Go runtime fatal（recover 无法拦截，
	// 整个应用崩溃）。本文件中所有持锁点统一走 WithSessionLock。

	// 若用户未回答提问便发新消息：自动标记跳过
	if !resume && st.PendingQuestion != nil {
		WithSessionLock(projectID, func() {
			SkipPendingQuestion(st, projectID, tid, s.MidiFiles, taskRec.Legacy)
		})
	}

	// 绑定工作区同步
	if project.GetWorkspaceDir(projectID) != "" {
		WithSessionLock(projectID, func() {
			s.MidiFiles = project.SyncWorkspaceToProjects(projectID, s.MidiFiles)
		})
	}

	// 处理用户消息
	var userMsgSnapshot []map[string]any
	if !resume {
		WithSessionLock(projectID, func() {
			st.UndoStack = append(st.UndoStack, UndoEntry{
				Files: SnapshotMidiFiles(s.MidiFiles),
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
			userMsgSnapshot = SnapshotDisplay(st.ChatDisplay)
		})
	} else {
		// resume 路径只读快照：绝不把活切片直接交给锁外回调序列化，
		// 否则与其它持锁写入方并发读写 map 会触发 runtime fatal
		WithSessionLock(projectID, func() {
			userMsgSnapshot = SnapshotDisplay(st.ChatDisplay)
		})
	}

	_ = onEvent(map[string]any{
		"type":     "chat",
		"messages": userMsgSnapshot,
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
	endedWithToolCalls := false
	for round := 0; round < config.MaxToolRounds; round++ {
		if tasks.TaskIsCancelled(tid) {
			break
		}
		endedWithToolCalls = false

		var apiMessages []llm.ChatCompletionMessage
		WithSessionLock(projectID, func() {
			apiMessages = buildApiMessages(st.FullHistory, s.MidiFiles)

			// 检查是否需要压缩并立即重新构建 API 消息
			if ShouldCompact(apiMessages) {
				st.FullHistory = CompactFullHistory(st.FullHistory)
				apiMessages = buildApiMessages(st.FullHistory, s.MidiFiles)
			}
		})

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
			WithSessionLock(projectID, func() {
				project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
			})
			// 错误退出而非轮数耗尽：不计入循环结束后的补提示条件
			endedWithToolCalls = false
			break
		}

		// 消费流式响应（processStreamChunks 内部经 withFLock 使用会话锁）
		deltaContent, deltaReasoning, toolCalls, err := processStreamChunks(streamResp, st, GetSessionLock(projectID), onEvent, tid)
		_ = streamResp.Body.Close()
		cancelled := tasks.TaskIsCancelled(tid) || taskCtx.Err() != nil
		if err != nil {
			slog.Error("处理流式响应失败", "err", err)
		}
		if err != nil || cancelled {
			// 停止/断流：保留已生成的部分内容并落盘（停止按钮的语义就是
			// "终止并保留已输出的内容"；断流同理，避免已生成内容丢失），
			// 丢弃半截工具调用（参数 JSON 可能被截断，执行会产生意外副作用）
			WithSessionLock(projectID, func() {
				if deltaContent != "" || deltaReasoning != "" {
					st.FullHistory = append(st.FullHistory, map[string]any{
						"role":              "assistant",
						"content":           deltaContent,
						"reasoning_content": deltaReasoning,
					})
				}
				project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
			})
			if cancelled {
				_ = onEvent(map[string]any{"type": "error", "message": "任务已停止"})
			} else if err != nil {
				_ = onEvent(map[string]any{"type": "error", "message": fmt.Sprintf("AI 调用失败: %v", err)})
			}
			_ = onEvent(map[string]any{"type": "done"})
			// 错误/停止退出而非轮数耗尽：不计入循环结束后的补提示条件
			endedWithToolCalls = false
			break
		}

		if len(toolCalls) == 0 {
			// 无工具调用：回合结束
			var finalSnapshot []map[string]any
			WithSessionLock(projectID, func() {
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

				finalSnapshot = SnapshotDisplay(st.ChatDisplay)
				project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
			})
			_ = onEvent(map[string]any{
				"type":     "chat",
				"messages": finalSnapshot,
			})
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

		WithSessionLock(projectID, func() {
			st.FullHistory = append(st.FullHistory, map[string]any{
				"role":              "assistant",
				"content":           deltaContent,
				"reasoning_content": deltaReasoning,
				"tool_calls":        tcHistoryList,
			})
		})

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
					var invalidSnapshot []map[string]any
					WithSessionLock(projectID, func() {
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
						invalidSnapshot = SnapshotDisplay(st.ChatDisplay)
					})
					_ = onEvent(map[string]any{
						"type":     "chat",
						"messages": invalidSnapshot,
					})
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
				var questionSnapshot []map[string]any
				WithSessionLock(projectID, func() {
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

					questionSnapshot = SnapshotDisplay(st.ChatDisplay)
					project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
				})
				_ = onEvent(map[string]any{
					"type":     "chat",
					"messages": questionSnapshot,
				})
				pausedByQuestion = true
				break
			}

			// 工具「执行中」占位块：用户第一时间看到工具标签 + ⏳ 进度标记，
			// 完成后由完成块整体替换。
			pendingEntry := llm.FormatPendingToolEntry(fnName, rawArgs)
			var pendingSnapshot []map[string]any
			WithSessionLock(projectID, func() {
				st.ChatDisplay = append(st.ChatDisplay, map[string]any{
					"role":    "assistant",
					"content": pendingEntry,
				})
				pendingSnapshot = SnapshotDisplay(st.ChatDisplay)
			})
			_ = onEvent(map[string]any{
				"type":     "chat",
				"messages": pendingSnapshot,
			})

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

					var filesSnapshot []project.MidiFileInfo
					WithSessionLock(projectID, func() {
						var updated []project.MidiFileInfo
						for _, f := range s.MidiFiles {
							if f.Name != newInfo.Name && f.Path != newInfo.Path {
								updated = append(updated, f)
							}
						}
						updated = append(updated, newInfo)
						s.MidiFiles = updated
						filesSnapshot = SnapshotMidiFiles(s.MidiFiles)
					})
					_ = onEvent(map[string]any{
						"type":  "files",
						"files": filesSnapshot,
					})

					relToProj, _ := filepath.Rel(config.ProjectRoot, targetPath)
					_ = onEvent(map[string]any{
						"type": "download",
						"url":  "/api/files/download?path=" + url.QueryEscape(filepath.ToSlash(relToProj)),
					})
				}
			} else if fnName == "delete_midi" {
				fn, _ := rawArgs["filename"].(string)
				cleanFn := filepath.ToSlash(filepath.Clean(fn))
				var deletedSnapshot []project.MidiFileInfo
				WithSessionLock(projectID, func() {
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
					deletedSnapshot = SnapshotMidiFiles(s.MidiFiles)
				})
				_ = onEvent(map[string]any{
					"type":  "files",
					"files": deletedSnapshot,
				})
			}

			var completedSnapshot []map[string]any
			WithSessionLock(projectID, func() {
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
				completedSnapshot = SnapshotDisplay(st.ChatDisplay)
			})
			_ = onEvent(map[string]any{
				"type":     "chat",
				"messages": completedSnapshot,
			})
		}

		WithSessionLock(projectID, func() {
			project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
		})

		if pausedByQuestion {
			// 提问暂停不算轮数耗尽
			endedWithToolCalls = false
			break
		}
		// 本轮以工具调用收尾：若下一轮循环因轮数上限不再执行，
		// 循环结束后据此补提示
		endedWithToolCalls = true
	}

	// 工具轮数耗尽：循环自然结束且最后一轮仍在等下一轮 LLM 决策，
	// 此前静默退出，用户看到对话「无声结束」且无任何指引
	if endedWithToolCalls && !tasks.TaskIsCancelled(tid) && taskCtx.Err() == nil {
		hint := "⚠ 本轮已达到单次任务的工具调用次数上限，自动停止。请继续发送消息，我会接着完成剩余操作。"
		var hintSnapshot []map[string]any
		WithSessionLock(projectID, func() {
			st.FullHistory = append(st.FullHistory, map[string]any{"role": "assistant", "content": hint})
			st.ChatDisplay = append(st.ChatDisplay, map[string]any{"role": "assistant", "content": hint})
			hintSnapshot = SnapshotDisplay(st.ChatDisplay)
			project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
		})
		_ = onEvent(map[string]any{
			"type":     "chat",
			"messages": hintSnapshot,
		})
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
	if err := AcquireProjectChatLock(ctx, projectID); err != nil {
		_ = onEvent(map[string]any{"type": "error", "message": "任务已停止"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	s := GetSession(projectID)
	tid := taskRec.ID
	s.SwitchTask(tid)
	st := s.GetTaskState(tid)
	globalBPM := project.GetProjectBPM(projectID)

	if st.PendingQuestion == nil {
		ReleaseProjectChatLock(projectID)
		_ = onEvent(map[string]any{"type": "error", "message": "该提问已失效或已被回答"})
		_ = onEvent(map[string]any{"type": "done"})
		return nil
	}

	var pending, remCalls []map[string]any // pending 为提问载荷；remCalls 为待补执行调用
	var questions []map[string]any
	var tcID, ansText string
	WithSessionLock(projectID, func() {
		rawPending := st.PendingQuestion
		st.PendingQuestion = nil

		// 记录撤销快照（回答前状态）
		st.UndoStack = append(st.UndoStack, UndoEntry{
			Files: SnapshotMidiFiles(s.MidiFiles),
		})

		questions = NormalizeQuestions(rawPending["questions"])
		tcID, _ = rawPending["tool_call_id"].(string)

		if rcList, ok := rawPending["remaining_calls"].([]any); ok {
			for _, rc := range rcList {
				if rcMap, ok := rc.(map[string]any); ok {
					remCalls = append(remCalls, rcMap)
				}
			}
		} else if rcList, ok := rawPending["remaining_calls"].([]map[string]any); ok {
			remCalls = rcList
		}

		ansText = FormatAnswerResult(questions, answers)
		st.FullHistory = append(st.FullHistory, map[string]any{
			"role":         "tool",
			"tool_call_id": tcID,
			"name":         "ask_user_question",
			"content":      ansText,
		})

		MarkQuestionBlock(st.ChatDisplay, questionID, answers)
	})
	_ = pending

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
		var pendingSnapshot []map[string]any
		WithSessionLock(projectID, func() {
			st.ChatDisplay = append(st.ChatDisplay, map[string]any{
				"role":    "assistant",
				"content": pendingEntry,
			})
			pendingSnapshot = SnapshotDisplay(st.ChatDisplay)
		})
		_ = onEvent(map[string]any{
			"type":     "chat",
			"messages": pendingSnapshot,
		})

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
				var filesSnapshot []project.MidiFileInfo
				WithSessionLock(projectID, func() {
					var updated []project.MidiFileInfo
					for _, f := range s.MidiFiles {
						if f.Name != newInfo.Name && f.Path != newInfo.Path {
							updated = append(updated, f)
						}
					}
					updated = append(updated, newInfo)
					s.MidiFiles = updated
					filesSnapshot = SnapshotMidiFiles(s.MidiFiles)
				})
				_ = onEvent(map[string]any{"type": "files", "files": filesSnapshot})
				relToProj, _ := filepath.Rel(config.ProjectRoot, targetPath)
				_ = onEvent(map[string]any{
					"type": "download",
					"url":  "/api/files/download?path=" + url.QueryEscape(filepath.ToSlash(relToProj)),
				})
			}
		}

		var completedSnapshot []map[string]any
		WithSessionLock(projectID, func() {
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
			completedSnapshot = SnapshotDisplay(st.ChatDisplay)
		})
		_ = onEvent(map[string]any{
			"type":     "chat",
			"messages": completedSnapshot,
		})
	}
	var finalSnapshot []map[string]any
	WithSessionLock(projectID, func() {
		finalSnapshot = SnapshotDisplay(st.ChatDisplay)
		project.SaveHistory(projectID, st.FullHistory, s.MidiFiles, tid, taskRec.Legacy)
	})
	_ = onEvent(map[string]any{
		"type":     "chat",
		"messages": finalSnapshot,
	})

	ReleaseProjectChatLock(projectID)

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

// withFLock 在已获取的会话文件锁保护下执行 fn（defer 释放：fn 内 panic
// 时锁不被永久占住，panic 继续传播由上层 recover 兜底）。供没有 projectID
// 上下文、只持有锁指针的调用点（processStreamChunks）使用。
func withFLock(fl *sync.RWMutex, fn func()) {
	fl.Lock()
	defer fl.Unlock()
	fn()
}

func processStreamChunks(resp *http.Response, st *TaskState, fl *sync.RWMutex, onEvent StreamCallback, tid string) (string, string, []llm.ToolCall, error) {
	reader := bufio.NewReader(resp.Body)
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	toolCallMap := make(map[int]*llm.ToolCall)

	// 占位 assistant 消息（快照写入的目标索引：流式循环期间恒为末条——
	// 工具块由调用方在本函数返回后才追加，同项目对话锁保证无并发写入者）
	withFLock(fl, func() {
		st.ChatDisplay = append(st.ChatDisplay, map[string]any{
			"role":    "assistant",
			"content": "",
		})
	})
	placeholderIdx := len(st.ChatDisplay) - 1

	lastEmit := time.Now()
	sentReasoning, sentContent := 0, 0
	// 快照节流：流式期间周期性把已生成内容写入占位消息（与轮末最终写入
	// 同款显示格式），使 GET 快照（重进页面/后台续跑轮询）能读到部分内容
	// 而非空气泡；锁内仅一次字符串赋值，与快照 RLock 并发安全
	lastSnapshotAt := time.Now()
	snapReasoning, snapContent := 0, 0
	var streamErr error

	// 上游空闲看门狗：长时间收不到任何字节视为死连，主动关闭 body 打断
	// 阻塞的 ReadString——否则项目对话锁会被永久占住（此前只能靠用户点
	// 停止，且同任务复跑后停止也可能失效）。正常流式每个 chunk 都有字节，
	// 只在上游真正停滞（无任何 keepalive）时触发
	const streamIdleTimeout = 120 * time.Second
	idleTimer := time.AfterFunc(streamIdleTimeout, func() {
		_ = resp.Body.Close()
	})
	defer idleTimer.Stop()

	for {
		if tasks.TaskIsCancelled(tid) {
			break
		}

		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			// 上游断流/连接中断（含用户停止导致的 ctx 取消、空闲看门狗
			// 超时主动关 body）：与正常结束一样走统一收尾——已生成的
			// 部分内容保留展示并落盘
			streamErr = err
			break
		}
		// EOF 时 line 可能仍带最后一段未换行数据（SSE 规范允许省略结尾
		// 换行）：先正常处理本行再退出，不能直接 break 丢数据
		if line != "" {
			idleTimer.Reset(streamIdleTimeout)

			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				// 兼容 "data: xxx" 与 "data:xxx" 两种分隔写法
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload != "" && payload != "[DONE]" {
					var chunk llm.ChatCompletionChunk
					if jsonErr := json.Unmarshal([]byte(payload), &chunk); jsonErr == nil && len(chunk.Choices) > 0 {
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

						// 后台续跑可见性：200ms 节流写快照（独立于上方 8ms 增量帧
						// 节流），内容无新增时跳过，避免无谓的锁竞争
						if time.Since(lastSnapshotAt) >= 200*time.Millisecond {
							lastSnapshotAt = time.Now()
							r := reasoningBuilder.String()
							c := contentBuilder.String()
							if (r != "" || c != "") && (len(r) != snapReasoning || len(c) != snapContent) {
								snapReasoning, snapContent = len(r), len(c)
								withFLock(fl, func() {
									if placeholderIdx >= 0 && placeholderIdx < len(st.ChatDisplay) {
										st.ChatDisplay[placeholderIdx]["content"] = llm.FormatDisplayMessage(r, c)
									}
								})
							}
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
	}

	var finalSnapshot []map[string]any
	withFLock(fl, func() {
		if contentBuilder.Len() == 0 && reasoningBuilder.Len() == 0 {
			if len(st.ChatDisplay) > 0 && st.ChatDisplay[len(st.ChatDisplay)-1]["content"] == "" {
				st.ChatDisplay = st.ChatDisplay[:len(st.ChatDisplay)-1]
			}
		} else if len(st.ChatDisplay) > 0 {
			formatted := llm.FormatDisplayMessage(reasoningBuilder.String(), contentBuilder.String())
			st.ChatDisplay[len(st.ChatDisplay)-1]["content"] = formatted
			finalSnapshot = SnapshotDisplay(st.ChatDisplay)
		}
	})
	if finalSnapshot != nil {
		_ = onEvent(map[string]any{
			"type":     "chat",
			"messages": finalSnapshot,
		})
	}

	return contentBuilder.String(), reasoningBuilder.String(), mapToToolCallList(toolCallMap), streamErr
}

func mapToToolCallList(m map[int]*llm.ToolCall) []llm.ToolCall {
	if len(m) == 0 {
		return nil
	}
	// 按 index 升序输出：上游可能用 1-based 或跳号 index，按下标连续
	// 遍历会静默丢弃尾部工具调用
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	list := make([]llm.ToolCall, 0, len(keys))
	for _, k := range keys {
		list = append(list, *m[k])
	}
	return list
}
