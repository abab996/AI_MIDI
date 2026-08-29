package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newLocalRequest 构造带本机 Host 的测试请求。
// ServeHTTP 的来源校验（isAllowedHost）只接受 127.0.0.1/localhost/
// wails.localhost，而 httptest.NewRequest 默认 Host 为 example.com，
// 直连 Router 的单测统一走这里。
func newLocalRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "127.0.0.1"
	return req
}

// TestOriginGuard 来源校验中间件的单测：合法本机 Host/Origin 放行，
// 非法 Host（DNS rebinding）与外站 Origin（CSRF 简单请求）一律 403。
func TestOriginGuard(t *testing.T) {
	r := NewRouter(nil, nil)

	okReqs := []struct{ host, origin string }{
		{"127.0.0.1:7860", ""},
		{"localhost", ""},
		{"[::1]:7860", ""},
		{"wails.localhost", "http://wails.localhost"},
		{"127.0.0.1:7860", "http://127.0.0.1:3000"},
		{"127.0.0.1:7860", "http://localhost"},
		{"127.0.0.1:7860", "https://wails.localhost"},
	}
	for _, tc := range okReqs {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden {
			t.Fatalf("host=%q origin=%q 应放行, got %d", tc.host, tc.origin, w.Code)
		}
	}

	badReqs := []struct{ host, origin string }{
		{"evil.example.com", ""},                       // DNS rebinding
		{"127.0.0.1.evil.com", ""},                     // 子域 rebinding
		{"127.0.0.1:7860", "https://evil.example.com"}, // 跨站简单请求
		{"127.0.0.1:7860", "null"},                     // 沙箱 iframe
		{"example.com", "http://example.com"},          // httptest 默认值也必须拒绝
		{"wails.localhost.evil.com", "http://wails.localhost.evil.com"},
	}
	for _, tc := range badReqs {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("host=%q origin=%q 应 403, got %d", tc.host, tc.origin, w.Code)
		}
	}
}
