package server

import (
	"compress/gzip"
	"net/http"
	"os"
	"strings"

	"aimidi/internal/config"
)

type noCacheFileSystem struct {
	fs http.FileSystem
}

func (n noCacheFileSystem) Open(name string) (http.File, error) {
	return n.fs.Open(name)
}

/*
	gzip 响应包装：仅用于静态文本资源（.js/.css/.html）。Wails 模式下静态

资源由内建 assetserver 直接服务、本包装只覆盖 -browser 模式——此前
~370KB 脚本每次刷新全量重传且 no-store。
*/
type gzipResponseWriter struct {
	http.ResponseWriter
	gz        *gzip.Writer
	wroteGzip bool
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.wroteGzip {
		g.wroteGzip = true
		g.Header().Del("Content-Length")
		g.Header().Set("Content-Encoding", "gzip")
	}
	return g.gz.Write(b)
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if !g.wroteGzip {
		g.wroteGzip = true
		g.Header().Del("Content-Length")
		g.Header().Set("Content-Encoding", "gzip")
	}
	g.ResponseWriter.WriteHeader(code)
}

func (r *Router) registerStaticRoutes() {
	var fileServer http.Handler

	if r.assetsFS != nil {
		fileServer = http.FileServer(http.FS(r.assetsFS))
	} else if _, err := os.Stat(config.WebDir); err == nil {
		fileServer = http.FileServer(http.Dir(config.WebDir))
	} else {
		return
	}

	textAsset := func(p string) bool {
		return strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") ||
			strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".svg")
	}

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// HTML/入口：no-cache（改版立即生效）；版本化静态资源：短缓存
		if textAsset(req.URL.Path) && req.URL.Path != "/" {
			w.Header().Set("Cache-Control", "public, max-age=60")
		} else {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		}

		// 文本资源且客户端支持且非 Range 请求：gzip 压缩
		if textAsset(req.URL.Path) && req.Method == http.MethodGet &&
			req.Header.Get("Range") == "" &&
			strings.Contains(req.Header.Get("Accept-Encoding"), "gzip") {
			gz := gzip.NewWriter(w)
			defer gz.Close()
			gzw := &gzipResponseWriter{ResponseWriter: w, gz: gz}
			fileServer.ServeHTTP(gzw, req)
			return
		}

		fileServer.ServeHTTP(w, req)
	})

	r.mux.Handle("/", wrapped)
}
