package llm

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestClassifyUpstreamErrorStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   string
	}{
		{401, `{"error":"invalid api key"}`, ErrCodeAuth},
		{403, `{"error":"permission denied"}`, ErrCodeAuth},
		{402, `{"error":"Insufficient Balance"}`, ErrCodeAuth},
		{429, `{"error":"rate limit exceeded"}`, ErrCodeRateLimit},
		{400, `{"error":"max_tokens is too large"}`, ErrCodeParam},
		{404, `{"error":"model not found"}`, ErrCodeParam},
		{500, `<html>server error</html>`, ErrCodeUpstream},
		{503, `service unavailable`, ErrCodeUpstream},
	}
	for _, c := range cases {
		ue := ClassifyUpstreamError(c.status, []byte(c.body), nil)
		if ue.Code != c.code {
			t.Errorf("status %d: code = %s, want %s (msg=%s)", c.status, ue.Code, c.code, ue.Message)
		}
		if ue.Message == "" {
			t.Errorf("status %d: empty message", c.status)
		}
		if len(ue.Detail) > 500 {
			t.Errorf("status %d: detail not clipped (%d chars)", c.status, len(ue.Detail))
		}
	}
}

func TestClassifyUpstreamErrorNetwork(t *testing.T) {
	ue := ClassifyUpstreamError(0, nil, &url.Error{Op: "Post", URL: "https://x", Err: errors.New("dial tcp: connection refused")})
	if ue.Code != ErrCodeNetwork {
		t.Fatalf("code = %s, want network", ue.Code)
	}
	ue = ClassifyUpstreamError(0, nil, errors.New("Get \"https://x\": context deadline exceeded"))
	if ue.Code != ErrCodeTimeout {
		t.Fatalf("code = %s, want timeout", ue.Code)
	}
	// 空闲看门狗关 body 的读错误归为超时而非网络
	ue = ClassifyUpstreamError(0, nil, errors.New("http: read on closed response body"))
	if ue.Code != ErrCodeTimeout {
		t.Fatalf("code = %s, want timeout", ue.Code)
	}
	// context.Canceled 是用户主动取消
	ue = ClassifyUpstreamError(0, nil, errors.New("context canceled"))
	if ue.Code != ErrCodeCancelled {
		t.Fatalf("code = %s, want cancelled", ue.Code)
	}
	// 网络错误时应为中文可读摘要而非原始英文直出
	if strings.Contains(ue.Message, "dial tcp") {
		t.Fatalf("raw network error leaked: %s", ue.Message)
	}
}

func TestUpstreamErrorAsUnwrap(t *testing.T) {
	inner := ClassifyUpstreamError(429, []byte("rate limited"), nil)
	wrapped := errors.New("outer")
	_ = wrapped
	// emitAIError 用 errors.As 从包装错误中提取分类
	var ue *UpstreamError
	if !errors.As(error(inner), &ue) {
		t.Fatal("errors.As should match *UpstreamError")
	}
	if ue.Code != ErrCodeRateLimit {
		t.Fatalf("code = %s", ue.Code)
	}
}
