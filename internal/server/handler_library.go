package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"aimidi/internal/config"
	"aimidi/internal/mcp"
)

// 用户知识库文件（Library/user/）管理端点，设置页「知识库」分区使用。
// 写操作（上传/删除/重命名）全部锁定在 Library/user/ 内——内置知识文件
// （Library 根目录）随安装包分发，只读不可写。

const (
	// maxKnowledgeBytes 单个用户知识文件上限：纯文本 2MB（约百万汉字）
	// 远超正常体量，超出只会白白撑大 AI 上下文
	maxKnowledgeBytes = 2 << 20
	// maxUserLibraryFiles 用户知识文件数量上限：每个文件都会在 AI 的
	// system prompt 清单里占一行，防止清单无限膨胀
	maxUserLibraryFiles = 100
	// maxPreviewBytes 预览返回的最大字节数（超长截断，前端提示"已截断"）
	maxPreviewBytes = 64 << 10
)

// handleLibrarySub /api/library/ 子路由：用户知识文件列表/上传/删除/重命名/
// 内容预览/打开目录
func (r *Router) handleLibrarySub(w http.ResponseWriter, req *http.Request) {
	switch strings.TrimPrefix(req.URL.Path, "/api/library/") {
	case "files":
		switch req.Method {
		case http.MethodGet:
			r.handleLibraryList(w, req)
		case http.MethodPost:
			r.handleLibraryUpload(w, req)
		case http.MethodDelete:
			r.handleLibraryDelete(w, req)
		default:
			writeError(w, http.StatusMethodNotAllowed, "仅支持 GET / POST / DELETE")
		}
	case "files/content":
		if req.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "仅支持 GET")
			return
		}
		r.handleLibraryContent(w, req)
	case "files/rename":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
			return
		}
		r.handleLibraryRename(w, req)
	case "open":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
			return
		}
		r.handleLibraryOpen(w, req)
	default:
		writeError(w, http.StatusNotFound, "未知的知识库端点")
	}
}

// sanitizeKnowledgeName 校验并消毒用户知识文件名：必须带 .md/.txt 后缀
// （大小写不敏感，统一小写化），词干经 sf2NameRe 消毒（保留各语言字母/
// 数字/_-.，中文文件名合法）。返回空串表示非法。
func sanitizeKnowledgeName(name string) string {
	name = strings.TrimSpace(name)
	base := filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(base))
	if ext != ".md" && ext != ".txt" {
		return ""
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = sf2NameRe.ReplaceAllString(stem, "_")
	stem = strings.TrimRight(stem, "._ ")
	if strings.TrimSpace(stem) == "" {
		return ""
	}
	return stem + ext
}

// withinLibraryUser 白名单：路径必须落在 Library/user 内（防逃逸）
func withinLibraryUser(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	base, err := filepath.Abs(config.LibraryUserDir)
	if err != nil {
		return false
	}
	return isSubPath(abs, base)
}

// libraryFileInfo 设置页知识文件列表项（modified 为 Unix 毫秒）
type libraryFileInfo struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

// scanKnowledgeFiles 列出目录下的知识文件（.md/.txt），按名称排序。
// 目录不存在/不可读视为空列表（内置目录缺失时前端只见空态，不报错）
func scanKnowledgeFiles(dir string) []libraryFileInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []libraryFileInfo
	for _, e := range entries {
		if e.IsDir() || !mcp.IsKnowledgeFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, libraryFileInfo{
			Name:     e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().UnixMilli(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// countKnowledgeFiles 统计目录下知识文件数（目录不存在按 0）
func countKnowledgeFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && mcp.IsKnowledgeFile(e.Name()) {
			n++
		}
	}
	return n
}

// handleLibraryList GET /api/library/files
// 返回内置（Library 根目录）与用户（Library/user/）两组知识文件。
// 内置组仅用于设置页只读展示；写操作全部限定在用户组目录内。
func (r *Router) handleLibraryList(w http.ResponseWriter, req *http.Request) {
	builtin := scanKnowledgeFiles(config.LibraryDir)
	user := scanKnowledgeFiles(config.LibraryUserDir)
	if builtin == nil {
		builtin = []libraryFileInfo{}
	}
	if user == nil {
		user = []libraryFileInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"builtin": builtin, "user": user})
}

// handleLibraryUpload POST /api/library/files?name=xxx.md
// 接收前端上传的知识文件原始字节（UTF-8 文本），落盘到 Library/user/ 供
// AI 清单收录。文件名即内容概括，AI 据此判断是否 read_library_file。
func (r *Router) handleLibraryUpload(w http.ResponseWriter, req *http.Request) {
	// handler 层再钳一次：路由层的 requestLimitFor 是共享默认值，
	// 显式收紧行为不依赖路由表（与 SF2 上传双层防线同思路）
	req.Body = http.MaxBytesReader(w, req.Body, maxKnowledgeBytes)

	safe := sanitizeKnowledgeName(req.URL.Query().Get("name"))
	if safe == "" {
		writeError(w, http.StatusBadRequest, "文件名无效：仅支持 .md / .txt 文件")
		return
	}

	buf, err := io.ReadAll(req.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "知识文件超过 2MB 上限")
			return
		}
		writeErr(w, http.StatusBadRequest, "接收上传数据失败", err)
		return
	}
	if len(buf) == 0 {
		writeError(w, http.StatusBadRequest, "文件内容为空")
		return
	}
	// 知识文件直接进入 AI 上下文，非 UTF-8（如 GBK）会变成乱码污染对话
	if !utf8.Valid(buf) {
		writeError(w, http.StatusBadRequest, "不是有效的 UTF-8 文本，请先将文件另存为 UTF-8 编码再上传")
		return
	}

	dir := config.LibraryUserDir
	if countKnowledgeFiles(dir) >= maxUserLibraryFiles {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("用户知识文件已达 %d 个上限，请先删除部分文件", maxUserLibraryFiles))
		return
	}

	_ = os.MkdirAll(dir, 0o755)
	dest := filepath.Join(dir, safe)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败", err)
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败", err)
		return
	}
	slog.Info("用户知识文件已保存", "file", dest, "bytes", len(buf))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": safe, "bytes": len(buf)})
}

// handleLibraryDelete DELETE /api/library/files?name=xxx.md
// 删除 Library/user/ 内的知识文件（同名消毒 + 白名单校验）
func (r *Router) handleLibraryDelete(w http.ResponseWriter, req *http.Request) {
	safe := sanitizeKnowledgeName(req.URL.Query().Get("name"))
	if safe == "" {
		writeError(w, http.StatusBadRequest, "文件名无效：仅支持删除 .md / .txt 文件")
		return
	}
	dest := filepath.Join(config.LibraryUserDir, safe)
	if !withinLibraryUser(dest) {
		writeError(w, http.StatusForbidden, "非法路径")
		return
	}

	removed := false
	if err := os.Remove(dest); err == nil {
		removed = true
	} else if !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, "删除失败", err)
		return
	}
	_ = os.Remove(dest + ".tmp") // 上传中断残留
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": removed})
}

// handleLibraryContent GET /api/library/files/content?name=xxx.md&scope=user|builtin
// 设置页预览知识文件内容，超长截断。scope=user（默认）读 Library/user/，
// scope=builtin 只读内置 Library 根目录；两种范围都只接受裸文件名。
func (r *Router) handleLibraryContent(w http.ResponseWriter, req *http.Request) {
	safe := sanitizeKnowledgeName(req.URL.Query().Get("name"))
	if safe == "" {
		writeError(w, http.StatusBadRequest, "文件名无效：仅支持 .md / .txt 文件")
		return
	}
	dir := config.LibraryUserDir
	if req.URL.Query().Get("scope") == "builtin" {
		dir = config.LibraryDir
	}
	data, err := os.ReadFile(filepath.Join(dir, safe))
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "文件不存在")
		} else {
			writeErr(w, http.StatusInternalServerError, "读取失败", err)
		}
		return
	}
	truncated := false
	if len(data) > maxPreviewBytes {
		data = data[:maxPreviewBytes]
		// 截断可能落在多字节 UTF-8 字符中间，回退到 rune 边界
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		truncated = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": safe, "content": string(data), "truncated": truncated})
}

// handleLibraryRename POST /api/library/files/rename  body: {"from","to"}
// 重命名用户知识文件（文件名即内容概括，改名即改 AI 眼中的主题描述）
func (r *Router) handleLibraryRename(w http.ResponseWriter, req *http.Request) {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的请求")
		return
	}
	from := sanitizeKnowledgeName(in.From)
	to := sanitizeKnowledgeName(in.To)
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "文件名无效：仅支持 .md / .txt 文件")
		return
	}
	src := filepath.Join(config.LibraryUserDir, from)
	dst := filepath.Join(config.LibraryUserDir, to)
	if !withinLibraryUser(src) || !withinLibraryUser(dst) {
		writeError(w, http.StatusForbidden, "非法路径")
		return
	}
	if _, err := os.Stat(dst); err == nil {
		writeError(w, http.StatusConflict, "已存在同名文件「"+to+"」")
		return
	}
	if err := os.Rename(src, dst); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "源文件不存在")
		} else {
			writeErr(w, http.StatusInternalServerError, "重命名失败", err)
		}
		return
	}
	slog.Info("用户知识文件已重命名", "from", from, "to", to)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": to})
}

// handleLibraryOpen POST /api/library/open
// 资源管理器打开 Library/user/（便于用外部编辑器批量管理知识文件）
func (r *Router) handleLibraryOpen(w http.ResponseWriter, req *http.Request) {
	dir := config.LibraryUserDir
	// 目录不存在时先创建（否则 explorer 打不开）
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "无法创建知识库目录", err)
		return
	}
	if err := exec.Command("explorer", dir).Start(); err != nil {
		writeErr(w, http.StatusInternalServerError, "打开文件夹失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": dir})
}
