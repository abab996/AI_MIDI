package server

import (
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
func (r *Router) handleAudioSoundfonts(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}

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

	capHint := 0
	if req.ContentLength > 0 {
		capHint = int(req.ContentLength)
	}
	buf := make([]byte, 0, capHint)
	chunk := make([]byte, 32*1024)
	for {
		n, err := req.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	// SF2 魔数校验：RIFF....sfbk
	if len(buf) < 12 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "sfbk" {
		writeError(w, http.StatusBadRequest, "不是有效的 SF2 文件")
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
