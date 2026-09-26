package llm

import (
	"strings"

	"aimidi/internal/config"
)

// JoinEndpoint 将规范化后的 base_url 与 API 后缀拼接为完整端点。
//
// 兼容 base_url 末尾已包含版本段的情况：
//   - base_url 已以 /v1 结尾（如 https://api.example.com/v1）
//   - base_url 已以 /openai 结尾（如 Gemini OpenAI 兼容层
//     https://generativelanguage.googleapis.com/v1beta/openai）
//
// 此时后缀中的 /v1 前缀会被剥除，避免拼出 /v1/v1/... 导致上游 404。
func JoinEndpoint(baseURL, suffix string) string {
	if strings.HasPrefix(suffix, "/v1/") &&
		(strings.HasSuffix(baseURL, "/v1") || strings.HasSuffix(baseURL, "/openai")) {
		return baseURL + suffix[len("/v1"):]
	}
	return baseURL + suffix
}

// EndpointURL 按协议拼出聊天或模型列表的完整地址。
// apiPath 为空时补 /v1；apiPath 为 "/" 时接在站点根上，不再插入 /v1；
// 已经写明的前缀（如智谱 /api/paas/v4）原样接上资源名。
func EndpointURL(baseURL, apiPath, protocol, resource string) (string, error) {
	prefix, err := config.ValidateBaseURL(baseURL, apiPath)
	if err != nil {
		return "", err
	}
	return prefix + endpointSuffix(prefix, apiPath, protocol, resource), nil
}

func endpointSuffix(prefix, apiPath, protocol, resource string) string {
	tail := "/chat/completions"
	if resource == "models" {
		tail = "/models"
	}
	if config.NormalizeProtocol(protocol) == config.ProtocolAnthropic && resource != "models" {
		tail = "/messages"
	}
	path := strings.TrimSpace(apiPath)
	if path == "/" {
		return tail
	}
	explicit := path != ""
	endsVersion := strings.HasSuffix(prefix, "/v1") || strings.HasSuffix(prefix, "/openai")
	if !explicit && !endsVersion {
		return "/v1" + tail
	}
	return tail
}
