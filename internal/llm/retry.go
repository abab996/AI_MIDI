package llm

import (
	"strings"
)

const (
	UpstreamMaxRetries = 3
	RetryWaitSeconds   = 5
)

// IsUpstreamTransient 判断是否为上游瞬时故障（限流 / 超时 / 5xx / 网络中断）
func IsUpstreamTransient(err error, statusCode int) bool {
	if statusCode == 429 || statusCode == 500 || statusCode == 502 || statusCode == 503 || statusCode == 504 {
		return true
	}
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "429") ||
		strings.Contains(text, "500") ||
		strings.Contains(text, "502") ||
		strings.Contains(text, "503") ||
		strings.Contains(text, "504") ||
		strings.Contains(text, "rate") ||
		strings.Contains(text, "resource_exhausted") ||
		strings.Contains(text, "timed out") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "connection") ||
		strings.Contains(text, "service unavailable")
}
