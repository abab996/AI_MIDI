package server

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"aimidi/internal/config"
	"aimidi/internal/engine"
)

var sf2NameRe = regexp.MustCompile(`[^\p{L}\p{N}_\-\.]`)

// handleAudioSoundfonts POST /api/audio/soundfonts
// 接收前端加载的 SF2 原始字节，落盘到 Library/soundfonts/ 供原生引擎使用。
// 文件名经查询参数 name 传入；引擎就绪时顺带下发 loadSoundFont。
// DELETE 同路径：删除磁盘镜像（音源库"卸载"），与上传用同一套文件名消毒。
func (r *Router) handleAudioSoundfonts(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodDelete {
		r.handleAudioSoundfontDelete(w, req)
		return
	}
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST / DELETE")
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 256<<20) // 256MB 上限（大音色库如 98MB 的钢琴 SF2 需能镜像供原生引擎加载）

	name := strings.TrimSpace(req.URL.Query().Get("name"))
	if name == "" {
		name = "uploaded.sf2"
	}
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	safe := sf2NameRe.ReplaceAllString(base, "_")
	if strings.TrimSpace(safe) == "" {
		safe = "soundfont"
	}

	dir := filepath.Join(config.LibraryDir, "soundfonts")
	dest := filepath.Join(dir, safe+".sf2")

	// 预分配提示：Content-Length 客户端可控，必须钳制到上限内，
	// 否则声明超大 Content-Length 即触发无界内存分配
	const maxSF2 = int64(256 << 20)
	capHint := 0
	if req.ContentLength > 0 {
		if req.ContentLength > maxSF2 {
			capHint = int(maxSF2)
		} else {
			capHint = int(req.ContentLength)
		}
	}
	buf := make([]byte, 0, capHint)
	chunk := make([]byte, 32*1024)
	var readErr error
	for {
		n, err := req.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			readErr = err
			break
		}
	}
	// 读取错误（MaxBytesReader 超限/客户端中断）不得当作 EOF：
	// 否则半截文件只要头部魔数合法就会被当作有效 SF2 落盘
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(readErr, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "SF2 文件超过 256MB 上限")
		} else {
			writeError(w, http.StatusBadRequest, "接收上传数据失败，请重试")
		}
		return
	}
	// SF2 魔数校验：RIFF....sfbk
	if len(buf) < 12 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "sfbk" {
		writeError(w, http.StatusBadRequest, "不是有效的 SF2 文件")
		return
	}
	// RIFF size 一致性校验：buf[4:8] 为小端 RIFF 块长度（不含头部 8 字节）。
	// 头部完整但内容被截断的文件（网络中断恰好落在尾部等）size 字段与
	// 实收字节数不符——只校验魔数会让半截文件落盘、引擎加载解析失败
	declared := uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16 | uint32(buf[7])<<24
	if declared != uint32(len(buf))-8 {
		writeError(w, http.StatusBadRequest, "SF2 文件不完整（RIFF size 与实收数据不符）")
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}

	result := map[string]any{"saved": dest, "bytes": len(buf)}
	if sup := engine.Get(); sup != nil {
		// 支持 per-track 加载：?track=2
		trackStr := strings.TrimSpace(req.URL.Query().Get("track"))
		if trackStr != "" {
			if tr, err := strconv.Atoi(trackStr); err == nil && tr >= 0 && tr < 32 {
				if err := sup.LoadSoundFontTrack(tr, dest); err == nil {
					result["engine_loaded"] = true
					result["track"] = tr
				}
			}
		} else {
			if err := sup.LoadSoundFont(dest); err == nil {
				result["engine_loaded"] = true
			}
		}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleAudioSoundfontDelete DELETE /api/audio/soundfonts?name=xxx.sf2
// 删除磁盘镜像。此前"卸载音色"只删 IndexedDB 记录，Library/soundfonts/
// 里的镜像文件永久残留并被引擎当作默认音色加载。
func (r *Router) handleAudioSoundfontDelete(w http.ResponseWriter, req *http.Request) {
	name := strings.TrimSpace(req.URL.Query().Get("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "缺少 name 参数")
		return
	}
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	safe := sf2NameRe.ReplaceAllString(base, "_")
	if strings.TrimSpace(safe) == "" {
		writeError(w, http.StatusBadRequest, "无效的音色名称")
		return
	}

	dir := filepath.Join(config.LibraryDir, "soundfonts")
	dest := filepath.Join(dir, safe+".sf2")
	// 白名单：只允许删除音色库目录内的文件
	abs, _ := filepath.Abs(dest)
	dirAbs, _ := filepath.Abs(dir)
	if !isSubPath(abs, dirAbs) {
		writeError(w, http.StatusForbidden, "非法路径")
		return
	}

	removed := false
	if err := os.Remove(dest); err == nil {
		removed = true
	} else if !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	_ = os.Remove(dest + ".tmp") // 上传中断残留
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": removed})
}
