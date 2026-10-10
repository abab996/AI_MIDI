package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newLocalRequest 构造带本机 Host 与启动令牌的测试请求。
// ServeHTTP 的来源校验（isAllowedHost）只接受 127.0.0.1/localhost/
// wails.localhost，而 httptest.NewRequest 默认 Host 为 example.com，
// 直连 Router 的单测统一走这里；令牌同理（生产路径由 authfetch.js 注入）。
func newLocalRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "127.0.0.1"
	if testAuthToken != "" {
		req.Header.Set("X-MM-Token", testAuthToken)
	}
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

// TestAuthTokenRequired 启动令牌：缺少/错误令牌时 /api/ 非豁免路径 401。
// 这是挡"同机其他程序直连 HTTP API"的那一层（脚本请求不带 Origin，
// 此前的来源校验会把它当成可信的非浏览器客户端）。
func TestAuthTokenRequired(t *testing.T) {
	r := NewRouter(nil, nil)

	// 豁免路径不需要令牌（页面靠 bootstrap 拿令牌，健康检查无副作用）
	for _, p := range []string{"/api/health", "/api/version"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "127.0.0.1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("%s 应免令牌, got 401", p)
		}
	}

	// 普通 /api/ 路径：无令牌 401
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Host = "127.0.0.1"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401, got %d", w.Code)
	}

	// 错误令牌 401
	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Host = "127.0.0.1"
	req.Header.Set("X-MM-Token", "wrong-token")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401, got %d", w.Code)
	}

	// 正确令牌放行
	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.Host = "127.0.0.1"
	req.Header.Set("X-MM-Token", testAuthToken)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("正确令牌不应 401")
	}
}

// TestBootstrapRejectsCrossOrigin 跨站页面不能拿到启动令牌
func TestBootstrapRejectsCrossOrigin(t *testing.T) {
	r := NewRouter(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap.js", nil)
	req.Host = "127.0.0.1"
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("跨站取令牌应 403, got %d", w.Code)
	}
	if w.Body.Len() > 0 && strings.Contains(w.Body.String(), testAuthToken) {
		t.Fatalf("403 响应不得泄露令牌")
	}
}

// TestAllowOriginsTightensToRealOrigin 允许来源收窄到实际监听来源后，
// 同机其他端口的页面不再被当成可信调用方。
func TestAllowOriginsTightensToRealOrigin(t *testing.T) {
	r := NewRouter(nil, nil)
	r.AllowOrigins([]string{"http://127.0.0.1:7860", "http://wails.localhost"})

	allowed := []string{"", "http://127.0.0.1:7860", "http://wails.localhost"}
	for _, o := range allowed {
		if !r.originChecker(o) {
			t.Fatalf("origin=%q 应被允许", o)
		}
	}
	blocked := []string{
		"http://127.0.0.1:3000", // 同机其他端口（此前放行）
		"http://localhost:9999",
		"https://evil.example.com",
		"null",
	}
	for _, o := range blocked {
		if r.originChecker(o) {
			t.Fatalf("origin=%q 不应被允许", o)
		}
	}
}
