package llm

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

// 错误分类码：SSE error 事件与 REST 错误统一携带，前端可据此做针对性引导
// （如 code=auth 时提示去设置页检查密钥，code=rate_limit 时提示稍后再试）
const (
	ErrCodeAuth      = "auth"
	ErrCodeRateLimit = "rate_limit"
	ErrCodeNetwork   = "network"
	ErrCodeTimeout   = "timeout"
	ErrCodeParam     = "param"
	ErrCodeUpstream  = "upstream"
	ErrCodeCancelled = "cancelled"
)

// UpstreamError 分类后的上游调用错误。Message 为中文可操作摘要（直接展示给
// 用户），Detail 为截断后的上游原始响应体（排查用，不进界面主文案），
// Code 供前端做针对性引导。
type UpstreamError struct {
	Code    string `json:"code"`
	Status  int    `json:"status,omitempty"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *UpstreamError) Error() string { return e.Message }

// ClipUpstreamBody 截断上游响应体，避免网关整页 HTML 错误刷屏
func ClipUpstreamBody(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if max <= 0 {
		max = 500
	}
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ClassifyUpstreamError 把 HTTP 状态码 + 响应体 / 网络层错误归类为
// 用户可理解的错误。三参至少给一个；全部为零值时返回通用上游错误。
func ClassifyUpstreamError(status int, body []byte, err error) *UpstreamError {
	detail := ClipUpstreamBody(body, 500)

	// 1) 网络层错误优先（此时通常拿不到状态码）
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			strings.Contains(strings.ToLower(err.Error()), "context canceled") {
			return &UpstreamError{Code: ErrCodeCancelled, Message: "请求已取消"}
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return &UpstreamError{Code: ErrCodeTimeout, Message: "请求超时：模型响应过慢或网络不稳定，请重试或更换模型", Detail: detail}
		}
		if isTimeoutErr(err) {
			return &UpstreamError{Code: ErrCodeTimeout, Message: "请求超时：模型响应过慢或网络不稳定，请重试或更换模型", Detail: detail}
		}
		// 流式空闲看门狗主动关闭 body 时在读端表现为该错误：
		// 语义是「上游长时间没有吐任何字节」，归为超时而非网络故障
		if strings.Contains(strings.ToLower(err.Error()), "read on closed response body") {
			return &UpstreamError{Code: ErrCodeTimeout, Message: "上游长时间未响应，连接已中断，请重试", Detail: detail}
		}
		if !isStatusErr(err) {
			return &UpstreamError{Code: ErrCodeNetwork, Message: "网络连接失败，请检查网络连接或代理设置后重试", Detail: detail}
		}
	}

	// 2) 状态码分类（响应体关键词可修正分类）
	hint := strings.ToLower(detail)
	switch {
	case status == 401 || status == 403:
		return &UpstreamError{Code: ErrCodeAuth, Status: status, Message: "API 密钥无效或没有访问权限，请在设置页检查密钥后重试", Detail: detail}
	case status == 402:
		return &UpstreamError{Code: ErrCodeAuth, Status: status, Message: "API 余额不足或账户异常，请前往服务商后台检查", Detail: detail}
	case status == 404:
		return &UpstreamError{Code: ErrCodeParam, Status: status, Message: "模型或接口地址不存在（404），请检查模型名与 Base URL 设置", Detail: detail}
	case status == 429:
		return &UpstreamError{Code: ErrCodeRateLimit, Status: status, Message: "触发服务商限流或额度上限，已自动重试仍失败，请稍后再试", Detail: detail}
	case status == 400:
		return &UpstreamError{Code: ErrCodeParam, Status: status, Message: "请求被上游拒绝（参数或模型名可能不受支持），请检查模型与生成参数设置", Detail: detail}
	case status >= 500:
		return &UpstreamError{Code: ErrCodeUpstream, Status: status, Message: "服务商服务暂时不可用，已自动重试仍失败，请稍后再试", Detail: detail}
	}

	// 3) 未知状态码时靠响应体关键词兜底
	switch {
	case strings.Contains(hint, "invalid") && strings.Contains(hint, "key"),
		strings.Contains(hint, "unauthorized"), strings.Contains(hint, "authentication"),
		strings.Contains(hint, "permission"):
		return &UpstreamError{Code: ErrCodeAuth, Status: status, Message: "API 密钥无效或没有访问权限，请在设置页检查密钥后重试", Detail: detail}
	case strings.Contains(hint, "insufficient"), strings.Contains(hint, "balance"), strings.Contains(hint, "quota"):
		return &UpstreamError{Code: ErrCodeAuth, Status: status, Message: "API 余额不足或额度用尽，请前往服务商后台检查", Detail: detail}
	case strings.Contains(hint, "rate limit"), strings.Contains(hint, "rate_limit"), strings.Contains(hint, "too many requests"):
		return &UpstreamError{Code: ErrCodeRateLimit, Status: status, Message: "触发服务商限流或额度上限，请稍后再试", Detail: detail}
	case strings.Contains(hint, "overloaded"), strings.Contains(hint, "service unavailable"), strings.Contains(hint, "bad gateway"):
		return &UpstreamError{Code: ErrCodeUpstream, Status: status, Message: "服务商服务暂时不可用，请稍后再试", Detail: detail}
	}

	return &UpstreamError{Code: ErrCodeUpstream, Status: status, Message: "AI 调用失败，请稍后重试", Detail: detail}
}

// isTimeoutErr 兜底识别字符串形态的超时错误（不同封装层的报错文案不一致）
func isTimeoutErr(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "timed out") ||
		strings.Contains(s, "deadline exceeded") ||
		strings.Contains(s, "tls handshake timeout")
}

// isStatusErr 判断错误是否为「拿到了响应但状态码非 2xx」一类的 HTTP 错误：
// 这类错误应走状态码分类而非网络分类。url.Error 包裹网络层错误时
// 其 Err 即底层原因
func isStatusErr(err error) bool {
	var ue *url.Error
	if errors.As(err, &ue) {
		// url.Error 本身通常代表传输层失败（连接/读写），仍走网络分类
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "status code") || strings.Contains(s, "http ")
}
