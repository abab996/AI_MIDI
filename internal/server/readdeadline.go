package server

// 非 SSE 路径的请求体读截止（Low 加固 2026-11-01）。
//
// 此前 http.Server 全局不设 ReadTimeout/WriteTimeout——WriteTimeout 会掐断
// SSE 分钟级长下行；而缺少读截止意味着慢速请求体（slowloris 变体）可以
// 无限占住连接与 handler。现在按路径精确豁免三个 SSE 端点，其余全部在
// handler 入口前用 ResponseController 设读截止：客户端拖着不把请求体
// 发完的连接最多存活 sseReadTimeoutGrace。
//
// 注意必须豁免的三个端点都是 POST+SSE（请求体很小、读得很快，但 handler
// 会在读完后挂住连接写长下行）：net/http 的后台读会在读截止到期时报超时
// 并拆除连接，长下行会被拦腰掐断。

import (
	"net/http"
	"time"
)

// requestReadDeadline 非 SSE 路径的请求体读截止
const requestReadDeadline = 120 * time.Second

// ssePaths SSE 长连接端点（读截止豁免）
var ssePaths = map[string]bool{
	"/api/chat":   true,
	"/api/answer": true,
	"/api/run":    true,
}

// WithRequestReadDeadline 给非 SSE 路径加请求体读截止。
func WithRequestReadDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !ssePaths[req.URL.Path] {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(requestReadDeadline))
		}
		next.ServeHTTP(w, req)
	})
}
