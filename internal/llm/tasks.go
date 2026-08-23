package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"aimidi/internal/config"
)

const (
	SystemPrompt = `现在你是一位精通乐理的音乐人，你需要根据用户的要求解决用户的问题。
由于无法直接上传midi文件，我们会使用类似midi文件的"note_table"格式来记录音符信息，以下位"note_table"的格式介绍：
[note: "<音符键名>", velocity: "<音符力度>", start: "<音符的开始时间（拍）>", end: "<音符的结束时间（拍）>" ]
示例 ：
[note: "C4", velocity: "80", start: "1", end: "2" ] 
表示音符对应的按键是C4，演奏力度80，时间是第一拍到第二拍。 `

	NoteTableOnlySuffix = `，必须给出完整可用的音符数据（和弦数据），禁止只给出部分示例,且最终回答中仅包含"note_table"格式的音符数据,禁止掺杂其它无关内容。`
)

// Chat 非流式统一调用通道
func Chat(userContent string, s config.Settings, timeoutSeconds int) (string, error) {
	apiKey := s.APIKey
	if apiKey == "" {
		apiKey = config.GetAPIKey()
	}
	if apiKey == "" {
		return "", fmt.Errorf("请先填写并保存 API Key")
	}

	baseURL := s.BaseURL
	if baseURL == "" {
		baseURL = config.DefaultBaseURL
	}
	validatedURL, err := config.ValidateBaseURL(baseURL, s.APIPath)
	if err != nil {
		return "", fmt.Errorf("无效的 Base URL: %w", err)
	}

	model := s.Model
	if model == "" {
		model = config.DefaultModel
	}

	isGemini := config.IsGeminiProvider(validatedURL)

	reqBody := ChatCompletionRequest{
		Model: model,
		Messages: []ChatCompletionMessage{
			{Role: "system", Content: SystemPrompt},
			{Role: "user", Content: userContent},
		},
		Stream: false,
	}

	if s.ReasoningEffort != "" {
		reqBody.ReasoningEffort = s.ReasoningEffort
	}
	if s.MaxTokens != nil {
		reqBody.MaxTokens = s.MaxTokens
	}
	if s.MaxCompletionTokens != nil {
		reqBody.MaxCompletionTokens = s.MaxCompletionTokens
	}
	if s.ThinkingEnabled && isGemini {
		reqBody.ExtraBody = map[string]any{
			"thinking": map[string]string{"type": "enabled"},
		}
	}

	endpoint := JoinEndpoint(validatedURL, "/v1/chat/completions")
	if timeoutSeconds <= 0 {
		timeoutSeconds = config.DefaultTimeoutSeconds
	}
	client := NewHTTPClient(time.Duration(timeoutSeconds) * time.Second)

	// 尝试带有参数剥除与重试的循环
	strippableSteps := []string{"extra_body", "reasoning_effort", "max_tokens", "max_completion_tokens"}
	stepIdx := 0

	for attempt := 0; attempt < UpstreamMaxRetries+len(strippableSteps)+1; attempt++ {
		jsonBytes, err := json.Marshal(reqBody)
		if err != nil {
			return "", err
		}

		httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(jsonBytes))
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := client.Do(httpReq)
		if err != nil {
			if IsUpstreamTransient(err, 0) && attempt < UpstreamMaxRetries {
				slog.Warn("上游瞬时故障，等待重试...", "attempt", attempt+1)
				time.Sleep(time.Duration(RetryWaitSeconds) * time.Second)
				continue
			}
			return "", err
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return "", readErr
		}

		if resp.StatusCode == http.StatusOK {
			var completion ChatCompletionResponse
			if err := json.Unmarshal(respBody, &completion); err != nil {
				return "", fmt.Errorf("解析响应 JSON 失败: %w", err)
			}
			if len(completion.Choices) > 0 {
				return completion.Choices[0].Message.Content, nil
			}
			return "", fmt.Errorf("上游返回空的 choice 列表")
		}

		if resp.StatusCode == http.StatusBadRequest {
			// 剥除不兼容参数重试
			if stepIdx < len(strippableSteps) {
				param := strippableSteps[stepIdx]
				stepIdx++
				slog.Warn("API 返回 400，剥除参数后重试", "param", param)
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
			return "", fmt.Errorf("API 返回 400: %s", string(respBody))
		}

		if IsUpstreamTransient(nil, resp.StatusCode) && attempt < UpstreamMaxRetries {
			slog.Warn("上游状态码故障，等待重试...", "status", resp.StatusCode, "attempt", attempt+1)
			time.Sleep(time.Duration(RetryWaitSeconds) * time.Second)
			continue
		}

		return "", fmt.Errorf("API 请求失败 (%d): %s", resp.StatusCode, string(respBody))
	}

	return "", fmt.Errorf("重试次数耗尽，未能完成请求")
}

// AddChord 配和弦任务
func AddChord(noteTable, bpm, timeSig, requirements string, s config.Settings) (string, error) {
	content := fmt.Sprintf("音符数据：%s，BPM：%s，拍号：%s，现在你需要给这段旋律配上适合的和弦。%s。有如下要求：%s",
		noteTable, bpm, timeSig, NoteTableOnlySuffix, requirements)
	return Chat(content, s, 0)
}

// TranslateLyrics 翻译歌词任务
func TranslateLyrics(noteTable, lyrics, bpm, timeSig, origLang, targetLang string, s config.Settings) (string, error) {
	content := fmt.Sprintf("音符数据：%s，歌词数据：%s，BPM：%s，拍号：%s，现在你得到的是人声的旋律和一段%s歌词。你需要把这段%s歌词翻译成%s。并且使翻译后的歌词能够与人声旋律完美贴合。注意最终回答中只能包含翻译后的歌词原文（如果翻译的目标语言是日语，请在给出的翻译歌词后面列出其对应的平假名）。有如下要求：",
		noteTable, lyrics, bpm, timeSig, origLang, origLang, targetLang)
	return Chat(content, s, 0)
}

// DesignMelisma 设计转音任务
func DesignMelisma(noteTable, lyrics, bpm, timeSig, requirements string, s config.Settings) (string, error) {
	content := fmt.Sprintf("音符数据：%s，歌词数据：%s，BPM：%s，拍号：%s，你现在需要帮我设计转音%s。有如下要求：%s",
		noteTable, lyrics, bpm, timeSig, NoteTableOnlySuffix, requirements)
	return Chat(content, s, 0)
}

// OtherRequirements 其他要求自由任务
func OtherRequirements(noteTable, lyrics, bpm, timeSig, requirements string, noteOutput bool, s config.Settings) (string, error) {
	base := fmt.Sprintf("音符数据：%s，歌词数据：%s，BPM：%s，拍号：%s，现在你可能没有得到有效的音符或歌词数据（也有可能得到了有效数据），",
		noteTable, lyrics, bpm, timeSig)
	var content string
	if noteOutput {
		content = fmt.Sprintf("%s但是你现在需要输出音符文件，请根据以下要求完成任务：%s。请在最终回复中严格按照\"note_table\"格式输出音符数据%s。",
			base, requirements, NoteTableOnlySuffix)
	} else {
		content = fmt.Sprintf("%s但是你现在不用输出音符文件，请根据以下要求完成任务：%s，并给出回答",
			base, requirements)
	}
	return Chat(content, s, 0)
}
