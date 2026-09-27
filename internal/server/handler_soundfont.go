package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"melodymate/internal/config"
	"melodymate/internal/engine"
	"melodymate/internal/soundlib"
)

var sf2NameRe = regexp.MustCompile(`[^\p{L}\p{N}_\-\.]`)

// sf2UploadError 上传校验失败：status 为应答码，detail 为面向用户的说明
type sf2UploadError struct {
	status int
	detail string
}

// readSF2Upload 读取并校验上传的 SF2 字节流。
// 校验三层：魔数（RIFF....sfbk）、RIFF 声明长度与实收字节一致（挡半截文件）、
// 大小上限（MaxBytesReader 超限与客户端中断都必须显式失败，否则半截文件
// 只要头部合法就会被当作有效 SF2 落盘）。
func readSF2Upload(req *http.Request) ([]byte, *sf2UploadError) {
	const maxSF2 = int64(256 << 20)
	// w 参数仅用于向连接标记 requestTooLarge（类型断言失败即跳过），
	// 这里只要限流语义，传 nil 即可
	req.Body = http.MaxBytesReader(nil, req.Body, maxSF2)

	// 预分配提示：Content-Length 客户端可控，必须钳制到上限内，
	// 否则声明超大 Content-Length 即触发无界内存分配
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
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(readErr, &maxErr) {
			return nil, &sf2UploadError{http.StatusRequestEntityTooLarge, "SF2 文件超过 256MB 上限"}
		}
		return nil, &sf2UploadError{http.StatusBadRequest, "接收上传数据失败，请重试"}
	}
	// SF2 魔数校验：RIFF....sfbk
	if len(buf) < 12 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "sfbk" {
		return nil, &sf2UploadError{http.StatusBadRequest, "不是有效的 SF2 文件"}
	}
	// RIFF size 一致性校验：buf[4:8] 为小端 RIFF 块长度（不含头部 8 字节）。
	// 头部完整但内容被截断的文件（网络中断恰好落在尾部等）size 字段与
	// 实收字节数不符——只校验魔数会让半截文件落盘、引擎加载解析失败
	declared := uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16 | uint32(buf[7])<<24
	if declared != uint32(len(buf))-8 {
		return nil, &sf2UploadError{http.StatusBadRequest, "SF2 文件不完整（RIFF size 与实收数据不符）"}
	}
	return buf, nil
}

// writeSF2File 消毒文件名并原子落盘到 Library/soundfonts/，返回绝对路径
func writeSF2File(rawName string, buf []byte) (string, error) {
	base := filepath.Base(strings.TrimSpace(rawName))
	if base == "" || base == "." {
		base = "uploaded.sf2"
	}
	base = strings.TrimSuffix(base, filepath.Ext(base))
	safe := sf2NameRe.ReplaceAllString(base, "_")
	if strings.TrimSpace(safe) == "" {
		safe = "soundfont"
	}

	dir := filepath.Join(config.LibraryDir, "soundfonts")
	dest := filepath.Join(dir, safe+".sf2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// sf2DisplayName 从上传时提供的原始文件名取展示名（去扩展名，保留空格等）。
// 落盘名要消毒，展示名不必——用户在界面里看到的是自己选的文件名。
func sf2DisplayName(rawName string) string {
	base := filepath.Base(strings.TrimSpace(rawName))
	if base == "" || base == "." {
		return ""
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// handleAudioSoundfonts POST /api/audio/soundfonts
// 接收前端加载的 SF2 原始字节，落盘到 Library/soundfonts/ 供原生引擎使用。
// 文件名经查询参数 name 传入；引擎就绪时顺带下发 loadSoundFont。
// DELETE 同路径：删除磁盘镜像（音源库"卸载"），与上传用同一套文件名消毒。
//
// 上传同样会把文件登记进音源注册表：本入口是浏览器模式（WebAudio）与
// 旧界面的兼容通道，不登记的话机架/声音管理里看不到这个音色。
func (r *Router) handleAudioSoundfonts(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodDelete {
		r.handleAudioSoundfontDelete(w, req)
		return
	}
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST / DELETE")
		return
	}

	buf, uerr := readSF2Upload(req)
	if uerr != nil {
		writeError(w, uerr.status, uerr.detail)
		return
	}
	rawName := req.URL.Query().Get("name")
	dest, err := writeSF2File(rawName, buf)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}

	result := map[string]any{"saved": dest, "bytes": len(buf)}
	if src, created, err := soundlib.EnsureSF2Source(dest, sf2DisplayName(rawName)); err == nil {
		result["source"] = src
		result["registered"] = created
	} else {
		slog.Warn("SF2 已落盘但登记注册表失败", "path", dest, "err", err)
	}
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
	// 同步撤销注册表条目：只删文件会让"声音管理"里留下指向空路径的死条目
	if ok, err := soundlib.RemoveSF2ByPath(dest); err != nil {
		slog.Warn("删除 SF2 后撤销注册表条目失败", "path", dest, "err", err)
	} else if ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": removed, "unregistered": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": removed})
}
