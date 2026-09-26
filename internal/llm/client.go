package llm

import (
	"encoding/json"
	"net/http"
	"time"

	"aimidi/internal/config"
)

// ChatCompletionMessage 消息结构
type ChatCompletionMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ExtraContent     any        `json:"extra_content,omitempty"`
}

// ToolCall 单个工具调用
type ToolCall struct {
	Index        int              `json:"index,omitempty"`
	ID           string           `json:"id,omitempty"`
	Type         string           `json:"type,omitempty"`
	Function     FunctionCall     `json:"function"`
	ExtraContent *GoogleExtraBody `json:"extra_content,omitempty"`
}

// GoogleExtraBody Gemini thought_signature
type GoogleExtraBody struct {
	Google map[string]string `json:"google,omitempty"`
}

// FunctionCall 函数名与参数
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDefinition 工具声明定义
type ToolDefinition struct {
	Type     string         `json:"type"`
	Function FunctionSchema `json:"function"`
}

// FunctionSchema 函数元数据规范
type FunctionSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ChatCompletionRequest 请求结构
type ChatCompletionRequest struct {
	Model               string                  `json:"model"`
	Messages            []ChatCompletionMessage `json:"messages"`
	Tools               []ToolDefinition        `json:"tools,omitempty"`
	Stream              bool                    `json:"stream"`
	MaxTokens           *int                    `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                    `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string                  `json:"reasoning_effort,omitempty"`
	ExtraBody           map[string]any          `json:"extra_body,omitempty"`
}

// ChatCompletionResponse 非流式响应结构
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index        int                   `json:"index"`
		Message      ChatCompletionMessage `json:"message"`
		FinishReason string                `json:"finish_reason"`
	} `json:"choices"`
}

// ChatCompletionChunk 流式响应增量片段
type ChatCompletionChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string           `json:"role"`
			Content          any              `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []ToolCallChunk  `json:"tool_calls"`
			ExtraContent     *GoogleExtraBody `json:"extra_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// ToolCallChunk 流式增量中的工具调用
type ToolCallChunk struct {
	Index        int              `json:"index"`
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Function     FunctionCall     `json:"function"`
	ExtraContent *GoogleExtraBody `json:"extra_content"`
}

// NewHTTPClient 创建禁用重定向的客户端（防止 API Key 在重定向中泄露）
func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultTimeoutSeconds) * time.Second
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewStreamHTTPClient 创建专用于流式 SSE 的 HTTP 客户端
// 配置首包响应头超时与拨号超时，但不设置全局 Timeout 限制，防止长生成被强行掐断
func NewStreamHTTPClient(headerTimeout time.Duration) *http.Client {
	if headerTimeout <= 0 {
		headerTimeout = 60 * time.Second
	}
	transport := &http.Transport{
		ResponseHeaderTimeout: headerTimeout,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ModelListResponse /v1/models 响应
type ModelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// FetchModels 获取上游可用模型列表
func FetchModels(apiKey, baseURL, apiPath, protocol string) ([]string, string, error) {
	if baseURL == "" {
		baseURL = config.DefaultBaseURL
	}
	// 本地服务（Ollama / LM Studio / vLLM / llama.cpp）不校验密钥，空密钥照样能列模型
	needKey := config.RequiresAPIKey(baseURL)
	if apiKey == "" && needKey {
		apiKey = config.GetAPIKey()
	}
	if apiKey == "" && needKey {
		return nil, "⚠ 请先填写并保存 API Key", nil
	}

	if protocol == "" {
		protocol = config.ProtocolOpenAI
		if config.IsGeminiProvider(baseURL) {
			protocol = config.ProtocolGemini
		}
	}
	fullURL, err := EndpointURL(baseURL, apiPath, protocol, "models")
	if err != nil {
		return nil, "✗ 配置错误，请检查 Base URL 与 API 路径。", err
	}

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, "✗ 获取模型失败，请检查网络或 API Key。", err
	}
	/* 空密钥时不能发 "Bearer "（裸前缀），部分服务端会直接 400；
	   Anthropic 的 anthropic-version 仍要带，那是协议版本不是凭证 */
	if config.NormalizeProtocol(protocol) == config.ProtocolAnthropic {
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		req.Header.Set("anthropic-version", anthropicVersion)
	} else if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := NewHTTPClient(15 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "✗ 获取模型失败，请检查网络或 API Key。", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "✗ 获取模型失败，API 返回状态码: " + resp.Status, nil
	}

	var mResp ModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&mResp); err != nil {
		return nil, "✗ 解析模型列表失败", err
	}

	var ids []string
	for _, m := range mResp.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}

	if len(ids) == 0 {
		return nil, "⚠ 未获取到任何模型", nil
	}

	return ids, "", nil
}
