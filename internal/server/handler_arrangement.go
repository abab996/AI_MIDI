package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aimidi/internal/config"
)

// arrangeAudioExt 支持的音频素材扩展名 → MIME
var arrangeAudioExt = map[string]string{
	".wav":  "audio/wav",
	".wave": "audio/wav",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
}

// handleArrangementSub /api/arrangement/ 子路由分发
func (r *Router) handleArrangementSub(w http.ResponseWriter, req *http.Request) {
	sub := strings.TrimPrefix(req.URL.Path, "/api/arrangement/")
	switch {
	case sub == "pick-folder" && req.Method == http.MethodPost:
		path := ""
		if r.dialogFn != nil {
			if p, err := r.dialogFn(); err == nil {
				path = p
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": path})

	case sub == "dirs" && req.Method == http.MethodGet:
		dirs := config.LoadSettings().MaterialDirs
		if dirs == nil {
			dirs = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"dirs": dirs})

	case sub == "dirs" && req.Method == http.MethodPost:
		var in struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil || strings.TrimSpace(in.Path) == "" {
			writeError(w, http.StatusBadRequest, "目录路径不能为空")
			return
		}
		clean := filepath.Clean(strings.TrimSpace(in.Path))
		info, err := os.Stat(clean)
		if err != nil || !info.IsDir() {
			writeError(w, http.StatusBadRequest, "目录不存在或不可访问")
			return
		}
		s := config.LoadSettings()
		for _, d := range s.MaterialDirs {
			if strings.EqualFold(d, clean) {
				writeJSON(w, http.StatusOK, map[string]any{"dirs": s.MaterialDirs})
				return
			}
		}
		s.MaterialDirs = append(s.MaterialDirs, clean)
		if err := config.SaveSettings(s); err != nil {
			writeError(w, http.StatusInternalServerError, "保存素材目录失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"dirs": s.MaterialDirs})

	case sub == "dirs" && req.Method == http.MethodDelete:
		var in struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil || strings.TrimSpace(in.Path) == "" {
			writeError(w, http.StatusBadRequest, "目录路径不能为空")
			return
		}
		clean := filepath.Clean(strings.TrimSpace(in.Path))
		s := config.LoadSettings()
		out := make([]string, 0, len(s.MaterialDirs))
		for _, d := range s.MaterialDirs {
			if !strings.EqualFold(d, clean) {
				out = append(out, d)
			}
		}
		s.MaterialDirs = out
		if err := config.SaveSettings(s); err != nil {
			writeError(w, http.StatusInternalServerError, "保存素材目录失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"dirs": s.MaterialDirs})

	case sub == "files" && req.Method == http.MethodGet:
		r.handleArrangeListFiles(w, req)

	case sub == "audio" && req.Method == http.MethodGet:
		r.handleArrangeAudio(w, req)

	default:
		writeError(w, http.StatusNotFound, "未知接口")
	}
}

// registeredMaterialDir 校验 dir 是已注册素材目录，返回规范化路径
func registeredMaterialDir(dir string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if clean == "" || clean == "." {
		return "", false
	}
	for _, d := range config.LoadSettings().MaterialDirs {
		if strings.EqualFold(d, clean) {
			return d, true
		}
	}
	return "", false
}

// materialPathWithinRegistered 校验绝对路径位于某个已注册素材目录之内（防穿越）
func materialPathWithinRegistered(p string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(p))
	if clean == "" || clean == "." || !filepath.IsAbs(clean) {
		return "", false
	}
	for _, d := range config.LoadSettings().MaterialDirs {
		rel, err := filepath.Rel(d, clean)
		if err != nil || rel == ".." {
			continue
		}
		if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return clean, true
	}
	return "", false
}

// handleArrangeListFiles 列出素材目录下的子目录与音频文件（单层懒加载）
// GET /api/arrangement/files?dir=<注册目录>&sub=<相对子路径>
func (r *Router) handleArrangeListFiles(w http.ResponseWriter, req *http.Request) {
	root, ok := registeredMaterialDir(req.URL.Query().Get("dir"))
	if !ok {
		writeError(w, http.StatusBadRequest, "未注册的素材目录")
		return
	}
	sub := strings.TrimSpace(req.URL.Query().Get("sub"))
	if strings.Contains(sub, "..") || strings.ContainsAny(sub, "\x00") {
		writeError(w, http.StatusBadRequest, "非法的子路径")
		return
	}
	full := root
	if sub != "" {
		full = filepath.Join(root, filepath.FromSlash(sub))
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取目录失败")
		return
	}

	type item struct {
		Name string `json:"name"`
		Type string `json:"type"` // "dir" | "file"
		Size int64  `json:"size,omitempty"`
		Ext  string `json:"ext,omitempty"`
	}
	dirs := []item{}
	files := []item{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, item{Name: name, Type: "dir"})
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		mime, isAudio := arrangeAudioExt[ext]
		if !isAudio {
			continue
		}
		info, err := e.Info()
		size := int64(0)
		if err == nil {
			size = info.Size()
		}
		files = append(files, item{Name: name, Type: "file", Size: size, Ext: mime})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })

	writeJSON(w, http.StatusOK, map[string]any{
		"dir":   root,
		"sub":   sub,
		"dirs":  dirs,
		"files": files,
	})
}

// handleArrangeAudio 提供素材音频字节流（支持 Range，供 Web Audio 解码与试听）
// GET /api/arrangement/audio?p=<注册目录内的绝对路径>
func (r *Router) handleArrangeAudio(w http.ResponseWriter, req *http.Request) {
	p, ok := materialPathWithinRegistered(req.URL.Query().Get("p"))
	if !ok {
		writeError(w, http.StatusForbidden, "路径不在已注册的素材目录内")
		return
	}
	mime, isAudio := arrangeAudioExt[strings.ToLower(filepath.Ext(p))]
	if !isAudio {
		writeError(w, http.StatusBadRequest, "不支持的音频格式")
		return
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeFile(w, req, p)
}
