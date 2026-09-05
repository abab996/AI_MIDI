package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"

	"aimidi/internal/config"
)

// Router HTTP API 与静态资源路由器
type Router struct {
	mux      *http.ServeMux
	assetsFS fs.FS
	dialogFn func() (string, error)
}

// NewRouter 创建路由器实例
func NewRouter(assetsFS fs.FS, dialogFn func() (string, error)) *Router {
	r := &Router{
		mux:      http.NewServeMux(),
		assetsFS: assetsFS,
		dialogFn: dialogFn,
	}
	r.registerRoutes()
	return r
}

// localOriginRe 本地回环来源（保留本地多端口开发调试的跨域能力，
// 但不再对任意网站开放——CORS * 会让恶意页面读取本机 API 响应）
var localOriginRe = regexp.MustCompile(`^https?://(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$`)

// wailsOriginRe Wails WebView2 的固定来源（http://wails.localhost）
var wailsOriginRe = regexp.MustCompile(`^https?://wails\.localhost/?$`)

// isAllowedHost 校验 Host 头。本服务无鉴权且持有删除项目、读取设置等
// 高危端点，必须只接受本机界面使用的主机名——DNS rebinding 攻击会把
// Host 换成攻击者域名指向 127.0.0.1，这里直接拒绝。
func isAllowedHost(hostPort string) bool {
	host := strings.ToLower(strings.TrimSpace(hostPort))
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	switch host {
	case "127.0.0.1", "localhost", "::1", "wails.localhost":
		return true
	default:
		return false
	}
}

// isAllowedOrigin 校验 Origin 头。空 Origin = 非浏览器客户端或同源 GET
// （同源 fetch 不携带 Origin），放行；其余仅接受本机回环与 Wails 来源。
// 挡掉恶意网页借"简单请求"（GET/POST + 表单编码，不触发预检）直接
// 驱动删除项目/改设置/烧 API Key 等 CSRF 面。
func isAllowedOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	return localOriginRe.MatchString(origin) || wailsOriginRe.MatchString(origin)
}

// capturingWriter 记录响应是否已开始写出：panic 发生在 SSE 流中途时无法
// 再补写 500，此时仅记日志并断开；未写出时兜底返回统一 JSON 错误。
type capturingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (c *capturingWriter) WriteHeader(code int) {
	c.wrote = true
	c.ResponseWriter.WriteHeader(code)
}

func (c *capturingWriter) Write(b []byte) (int, error) {
	c.wrote = true
	return c.ResponseWriter.Write(b)
}

// Flush 透传给内层 ResponseWriter（SSE 依赖）
func (c *capturingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 透传连接劫持（WebSocket 升级等场景；内层不支持时如实返回错误，
// 保证包装层不成为中间件链的接口黑洞）
func (c *capturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := c.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("内层 ResponseWriter 不支持 Hijack")
	}
	return h.Hijack()
}

// requestLimitFor 按路径返回请求体上限。此前除个别 handler 外全部无上限：
// multipart 上传会把超限部分 spool 到临时目录（可填满磁盘），JSON decoder
// 会把整个请求体读进内存（无界分配）。统一在中间件层兜底。
func requestLimitFor(path, method string) int64 {
	switch {
	case path == "/api/parse":
		return 64 << 20 // MIDI 上传（handler 内存阈值 32MB，超限部分 spool 临时目录）
	case strings.HasPrefix(path, "/api/audio/"):
		return 48 << 20 // soundfont 上传（handler 内部 32MB）+ bounce tracks（5MB）
	case strings.HasPrefix(path, "/api/arrangement/"):
		return 8 << 20 // 编排数据（handler 内部 5MB）
	case method == http.MethodPost && strings.HasSuffix(path, "/files"):
		return 256 << 20 // 项目文件上传（ParseMultipartForm 128MB 内存阈值）
	default:
		return 2 << 20 // JSON/常规请求
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 来源校验（含 OPTIONS 预检）：非法 Host/Origin 一律 403
	if !isAllowedHost(req.Host) {
		writeError(w, http.StatusForbidden, "非法 Host")
		return
	}
	if !isAllowedOrigin(req.Header.Get("Origin")) {
		writeError(w, http.StatusForbidden, "非法来源")
		return
	}

	if origin := req.Header.Get("Origin"); origin != "" && localOriginRe.MatchString(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	}

	if req.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 请求体上限（见 requestLimitFor）
	req.Body = http.MaxBytesReader(w, req.Body, requestLimitFor(req.URL.Path, req.Method))

	// panic 恢复：任何 handler（含 SSE 协程）的 panic 在此兜底，
	// 避免单个坏请求把整个 http server 连带服务拖崩。
	cw := &capturingWriter{ResponseWriter: w}
	defer recoverPanic(cw, req)

	r.mux.ServeHTTP(cw, req)
}

// recoverPanic 在 ServeHTTP 的 defer 中调用；独立成函数便于单测。
// 响应未开始时兜底返回统一 500 JSON；SSE 流中途 panic 时仅记日志并断开。
func recoverPanic(cw *capturingWriter, req *http.Request) {
	rec := recover()
	if rec == nil {
		return
	}
	slog.Error("HTTP handler panic",
		"method", req.Method, "path", req.URL.Path,
		"panic", rec, "stack", string(debug.Stack()))
	if cw != nil && !cw.wrote {
		writeError(cw, http.StatusInternalServerError, "服务内部错误，请查看日志后重试")
	}
}

func (r *Router) registerRoutes() {
	// Settings & Health & Theme
	r.mux.HandleFunc("/api/health", r.handleHealth)
	r.mux.HandleFunc("/api/settings", r.handleSettings)
	r.mux.HandleFunc("/api/transport/prefs", r.handleTransportPrefs)
	r.mux.HandleFunc("/api/models", r.handleModels)
	r.mux.HandleFunc("/api/theme/switch", r.handleThemeSwitch)

	// 前端异常上报（window.onerror → 本地日志，便于用户报障自查）
	r.mux.HandleFunc("/api/client-error", r.handleClientError)

	// 版本信息（设置页 About 卡片）
	r.mux.HandleFunc("/api/version", r.handleVersion)

	// MIDI
	r.mux.HandleFunc("/api/parse", r.handleParseMIDI)
	r.mux.HandleFunc("/api/run", r.handleRunTask)

	// Files
	r.mux.HandleFunc("/api/files/download", r.handleDownloadFile)

	// Chat & Answer
	r.mux.HandleFunc("/api/chat", r.handleChat)
	r.mux.HandleFunc("/api/answer", r.handleAnswer)

	// Tasks
	r.mux.HandleFunc("/api/tasks", r.handleTasks)
	r.mux.HandleFunc("/api/tasks/", r.handleTasksSub)

	// Projects
	r.mux.HandleFunc("/api/projects", r.handleProjects)
	r.mux.HandleFunc("/api/projects/search", r.handleProjectsSearch)
	r.mux.HandleFunc("/api/projects/", r.handleProjectsSub)

	// Arrangement 编排窗口（素材目录注册表 + 项目编排数据）
	r.mux.HandleFunc("/api/arrangement/", r.handleArrangementSub)

	// Audio 原生音频引擎
	r.mux.HandleFunc("/api/audio/", r.handleAudioSub)

	// Wails 桌面运行时脚本：chat.html 显式引用 /wails/runtime.js 与
	// /wails/ipc.js 以启用事件桥。桌面模式由 Wails assetserver 先行拦截提供
	// 真实脚本；-browser 模式没有 Go 绑定，这里返回无害空 JS——否则 404 的
	// HTML 响应体被 <script> 执行会触发"脚本错误"toast 并污染错误上报。
	r.mux.HandleFunc("/wails/runtime.js", serveWailsStubJS)
	r.mux.HandleFunc("/wails/ipc.js", serveWailsStubJS)

	// Static Assets
	r.registerStaticRoutes()
}

// serveWailsStubJS 浏览器模式下的 Wails 运行时占位脚本（无操作）。
// window.go/window.runtime 保持未定义，前端 bridged 判定为 false 走 SSE。
func serveWailsStubJS(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("/* wails runtime unavailable in browser mode */\n"))
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}

// writeErr 统一错误出口：detail 面向用户展示；err 仅进服务端日志
// （保留原始上下文，用户报障时可按日志定位）。detail 为空时回退"操作失败"。
func writeErr(w http.ResponseWriter, status int, detail string, err error) {
	if detail == "" {
		detail = "操作失败"
	}
	if err != nil {
		slog.Error(detail, "err", err)
	} else {
		slog.Warn(detail)
	}
	writeError(w, status, detail)
}

// handleClientError 前端 window.onerror 上报：本地落日志。
// 仅记录不响应业务状态，避免异常上报再触发新的异常。
func (r *Router) handleClientError(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var in struct {
		Message string `json:"message"`
		Source  string `json:"source"`
		Line    int    `json:"line"`
		Column  int    `json:"column"`
		Page    string `json:"page"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil || strings.TrimSpace(in.Message) == "" {
		writeError(w, http.StatusBadRequest, "无效的上报")
		return
	}

	slog.Warn("前端异常",
		"page", in.Page, "source", in.Source,
		"line", in.Line, "col", in.Column, "message", in.Message)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleVersion 设置页 About 卡片数据源（版本由 main 从 wails.json 注入）
func (r *Router) handleVersion(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": config.AppVersion,
		"name":    config.WindowTitle,
	})
}

func sseEvent(data any) string {
	b, _ := json.Marshal(data)
	return fmt.Sprintf("data: %s\n\n", string(b))
}

func setupSSEHeaders(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if ok {
		flusher.Flush()
	}
	return flusher
}
