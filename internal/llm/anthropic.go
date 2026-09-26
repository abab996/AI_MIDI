package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"aimidi/internal/config"
)

const anthropicVersion = "2023-06-01"

type anthropicMsg struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicReq struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []anthropicMsg  `json:"messages"`
	Tools     []anthropicTool `json:"tools,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
	Thinking  map[string]any  `json:"thinking,omitempty"`
}

// outputTokenLimit Anthropic 侧的输出上限，就是请求里的 max_tokens。
// 优先取 MaxTokens：模型目录给 Claude 系填的也是 MaxTokens(8192)。若让 OpenAI
// 专有的 max_completion_tokens 优先，用户在生成页设过的全局"最大输出"会盖掉
// 模型建议值，把 32768 发给只接受 8192 的老模型 → 上游 400。
func outputTokenLimit(s config.Settings) int {
	if s.MaxTokens != nil && *s.MaxTokens > 0 {
		return *s.MaxTokens
	}
	if s.MaxCompletionTokens != nil && *s.MaxCompletionTokens > 0 {
		return *s.MaxCompletionTokens
	}
	return config.DefaultMaxTokens
}

func buildAnthropicRequest(s config.Settings, messages []ChatCompletionMessage, tools []ToolDefinition, stream, thinking bool) anthropicReq {
	system, msgs := convertAnthropicMessages(messages)
	maxTok := outputTokenLimit(s)
	req := anthropicReq{
		Model:     s.Model,
		MaxTokens: maxTok,
		System:    system,
		Messages:  msgs,
		Stream:    stream,
	}
	for _, t := range tools {
		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		req.Tools = append(req.Tools, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	if thinking {
		budget := 4096
		if budget >= maxTok {
			budget = maxTok / 2
		}
		if budget >= 1024 {
			req.Thinking = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
	}
	return req
}

func convertAnthropicMessages(messages []ChatCompletionMessage) (string, []anthropicMsg) {
	var system strings.Builder
	var out []anthropicMsg
	var toolBatch []any

	flushTools := func() {
		if len(toolBatch) == 0 {
			return
		}
		out = append(out, anthropicMsg{Role: "user", Content: toolBatch})
		toolBatch = nil
	}

	for _, m := range messages {
		switch m.Role {
		case "system":
			flushTools()
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(m.Content)
		case "tool":
			toolBatch = append(toolBatch, map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			})
		case "assistant":
			flushTools()
			var blocks []any
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					continue
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": parseToolInput(tc.Function.Arguments),
				})
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropicMsg{Role: "assistant", Content: blocks})
		default:
			flushTools()
			text := m.Content
			if strings.TrimSpace(text) == "" {
				text = " "
			}
			out = append(out, anthropicMsg{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": text},
			}})
		}
	}
	flushTools()
	out = compactAnthropic(out)
	if len(out) == 0 {
		out = []anthropicMsg{{Role: "user", Content: []any{map[string]any{"type": "text", "text": " "}}}}
	}
	return system.String(), out
}

func parseToolInput(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil || v == nil {
		return map[string]any{"raw": raw}
	}
	if _, ok := v.(map[string]any); !ok {
		return map[string]any{"value": v}
	}
	return v
}

func compactAnthropic(msgs []anthropicMsg) []anthropicMsg {
	var out []anthropicMsg
	for _, m := range msgs {
		if len(m.Content) == 0 {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Role == m.Role {
			out[len(out)-1].Content = append(out[len(out)-1].Content, m.Content...)
			continue
		}
		out = append(out, m)
	}
	return out
}

func anthropicHTTP(ctx context.Context, s config.Settings, body anthropicReq, stream bool) (*http.Response, error) {
	endpoint, err := EndpointURL(s.BaseURL, s.APIPath, config.ProtocolAnthropic, "chat")
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var httpReq *http.Request
	if ctx != nil {
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	} else {
		httpReq, err = http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// 本地部署（如 LM Studio 的 Anthropic 兼容层）可能不校验密钥，空值就别发空头
	if s.APIKey != "" {
		httpReq.Header.Set("x-api-key", s.APIKey)
	}
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	var client *http.Client
	if stream {
		client = NewStreamHTTPClient(180 * time.Second)
	} else {
		client = NewHTTPClient(time.Duration(config.ChatTimeoutSeconds) * time.Second)
	}
	return client.Do(httpReq)
}

// ChatAnthropic 非流式 Messages 调用，返回拼接后的文本。
func ChatAnthropic(userContent string, s config.Settings, timeoutSeconds int) (string, error) {
	// 非流式响应周期覆盖整个推理过程：思考型模型 60s 内几乎必然超不完，
	// 未显式给超时（调用方传 0）时放宽到 ChatTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = config.ChatTimeoutSeconds
	}
	msgs := []ChatCompletionMessage{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: userContent},
	}
	thinking := s.ThinkingEnabled
	thinkingDropped := false
	transientRetries := 0
	// 预算 = thinking 降级 1 次 + 瞬态重试 3 次 + 首次 = 5 次尝试
	for attempt := 0; attempt < UpstreamMaxRetries+2; attempt++ {
		body := buildAnthropicRequest(s, msgs, nil, false, thinking)
		ctx := context.Background()
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
		resp, err := anthropicHTTP(ctx, s, body, false)
		cancel()
		if err != nil {
			if transientRetries < UpstreamMaxRetries && IsUpstreamTransient(err, 0) {
				transientRetries++
				slog.Warn("Anthropic 上游瞬时故障，等待重试...", "attempt", transientRetries, "err", err)
				time.Sleep(time.Duration(RetryWaitSeconds) * time.Second)
				continue
			}
			return "", ClassifyUpstreamError(0, nil, err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return "", ClassifyUpstreamError(0, nil, readErr)
		}
		if resp.StatusCode == http.StatusOK {
			return anthropicText(respBody)
		}
		if resp.StatusCode == http.StatusBadRequest && thinking && !thinkingDropped {
			thinking = false
			thinkingDropped = true
			continue
		}
		if transientRetries < UpstreamMaxRetries && IsUpstreamTransient(nil, resp.StatusCode) {
			transientRetries++
			slog.Warn("Anthropic 上游状态码故障，等待重试...", "status", resp.StatusCode, "attempt", transientRetries)
			time.Sleep(time.Duration(RetryWaitSeconds) * time.Second)
			continue
		}
		return "", ClassifyUpstreamError(resp.StatusCode, respBody, nil)
	}
	return "", &UpstreamError{Code: ErrCodeUpstream, Message: "重试次数耗尽，未能完成请求，请稍后再试"}
}

func anthropicText(body []byte) (string, error) {
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("解析响应 JSON 失败: %w", err)
	}
	var b strings.Builder
	for _, c := range parsed.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("上游返回空的文本内容")
	}
	return b.String(), nil
}

// StreamAnthropic 把 Messages SSE 转成 OpenAI Chat 形态的 SSE，供现有流式解析器消费。
func StreamAnthropic(ctx context.Context, s config.Settings, messages []ChatCompletionMessage, tools []ToolDefinition) (*http.Response, error) {
	thinking := s.ThinkingEnabled
	thinkingDropped := false
	transientRetries := 0
	// 预算 = thinking 降级 1 次 + 瞬态重试 3 次 + 首次 = 5 次尝试。
	// 重试点全部在首字节消费之前（非 OK 直接读完整 body），重放安全
	for attempt := 0; attempt < UpstreamMaxRetries+2; attempt++ {
		body := buildAnthropicRequest(s, messages, tools, true, thinking)
		resp, err := anthropicHTTP(ctx, s, body, true)
		if err != nil {
			if transientRetries < UpstreamMaxRetries && IsUpstreamTransient(err, 0) {
				transientRetries++
				slog.Warn("Anthropic 流式上游瞬时故障，等待重试...", "attempt", transientRetries, "err", err)
				if !sleepRetry(ctx) {
					return nil, ClassifyUpstreamError(0, nil, err)
				}
				continue
			}
			return nil, ClassifyUpstreamError(0, nil, err)
		}
		if resp.StatusCode == http.StatusOK {
			return wrapAnthropicStream(resp), nil
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest && thinking && !thinkingDropped {
			thinking = false
			thinkingDropped = true
			continue
		}
		if transientRetries < UpstreamMaxRetries && IsUpstreamTransient(nil, resp.StatusCode) {
			transientRetries++
			slog.Warn("Anthropic 流式上游状态码故障，等待重试...", "status", resp.StatusCode, "attempt", transientRetries)
			if !sleepRetry(ctx) {
				return nil, ClassifyUpstreamError(resp.StatusCode, respBody, nil)
			}
			continue
		}
		return nil, ClassifyUpstreamError(resp.StatusCode, respBody, nil)
	}
	return nil, &UpstreamError{Code: ErrCodeUpstream, Message: "重试次数耗尽，未能完成流式请求，请稍后再试"}
}

func wrapAnthropicStream(up *http.Response) *http.Response {
	pr, pw := io.Pipe()
	go func() {
		defer up.Body.Close()
		// 翻译层中途遇错（如 Anthropic 流中 error 事件、上游断流）时把
		// 错误经管道传给读端：processStreamChunks 会拿到非 EOF 读错误，
		// 按失败收尾上报——而不是把半截回复当成完整回答
		if err := TranslateAnthropicSSE(up.Body, pw); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()
	h := make(http.Header)
	h.Set("Content-Type", "text/event-stream")
	return &http.Response{StatusCode: http.StatusOK, Header: h, Body: pr}
}

// TranslateAnthropicSSE 读取 Anthropic SSE，写出 OpenAI data: 行。
func TranslateAnthropicSSE(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	toolIndex := map[int]int{}
	nextTool := 0
	eventName := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		typ, _ := ev["type"].(string)
		if typ == "" {
			typ = eventName
		}
		switch typ {
		case "content_block_start":
			idx := jsonInt(ev["index"])
			block, _ := ev["content_block"].(map[string]any)
			if block == nil {
				continue
			}
			if block["type"] != "tool_use" {
				continue
			}
			ti := nextTool
			nextTool++
			toolIndex[idx] = ti
			writeOpenAIChunk(w, map[string]any{
				"tool_calls": []any{map[string]any{
					"index": ti,
					"id":    strAny(block["id"]),
					"type":  "function",
					"function": map[string]any{
						"name":      strAny(block["name"]),
						"arguments": "",
					},
				}},
			}, nil)
		case "content_block_delta":
			idx := jsonInt(ev["index"])
			delta, _ := ev["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			switch delta["type"] {
			case "text_delta":
				writeOpenAIChunk(w, map[string]any{"content": strAny(delta["text"])}, nil)
			case "thinking_delta":
				writeOpenAIChunk(w, map[string]any{"reasoning_content": strAny(delta["thinking"])}, nil)
			case "input_json_delta":
				ti, ok := toolIndex[idx]
				if !ok {
					ti = nextTool
					nextTool++
					toolIndex[idx] = ti
				}
				writeOpenAIChunk(w, map[string]any{
					"tool_calls": []any{map[string]any{
						"index": ti,
						"function": map[string]any{
							"arguments": strAny(delta["partial_json"]),
						},
					}},
				}, nil)
			}
		case "message_delta":
			delta, _ := ev["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			reason := strAny(delta["stop_reason"])
			if reason == "" {
				continue
			}
			finish := "stop"
			if reason == "tool_use" {
				finish = "tool_calls"
			}
			writeOpenAIChunk(w, map[string]any{}, finish)
		case "message_stop":
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		case "error":
			// Anthropic 流中错误事件（overloaded_error / api_error / 中断）：
			// 此前被静默吞掉 → 流 EOF → 半截回复被当成完整回答。现在转成
			// 分类错误经管道传播，读端按失败收尾并上报用户
			em, _ := ev["error"].(map[string]any)
			etype := strAny(em["type"])
			emsg := strAny(em["message"])
			if etype == "" {
				etype = "stream_error"
			}
			if emsg == "" {
				emsg = "上游在生成过程中报告错误"
			}
			code := ErrCodeUpstream
			if strings.Contains(etype, "overloaded") {
				code = ErrCodeRateLimit
			}
			return &UpstreamError{
				Code:    code,
				Message: "生成中断：" + emsg + "（回复可能不完整，请重试）",
				Detail:  etype + ": " + emsg,
			}
		}
	}
	return sc.Err()
}

func writeOpenAIChunk(w io.Writer, delta map[string]any, finish any) {
	chunk := map[string]any{
		"choices": []any{
			map[string]any{"index": 0, "delta": delta, "finish_reason": finish},
		},
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func strAny(v any) string {
	s, _ := v.(string)
	return s
}
