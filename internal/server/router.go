package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
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

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if origin := req.Header.Get("Origin"); origin != "" && localOriginRe.MatchString(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	}

	if req.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	r.mux.ServeHTTP(w, req)
}

func (r *Router) registerRoutes() {
	// Settings & Health & Theme
	r.mux.HandleFunc("/api/health", r.handleHealth)
	r.mux.HandleFunc("/api/settings", r.handleSettings)
	r.mux.HandleFunc("/api/models", r.handleModels)
	r.mux.HandleFunc("/api/theme/switch", r.handleThemeSwitch)

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

	// Static Assets
	r.registerStaticRoutes()
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
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
