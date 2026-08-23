package llm

import "strings"

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
