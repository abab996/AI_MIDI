package server

import (
	"crypto/sha256"
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

// maxSF2Upload 单文件上限：RIFF 的长度字段是 uint32，理论上限 4GB。
const maxSF2Upload = int64(4 << 30)

// fileSHA256 流式计算文件摘要（用于判断同名导入是否同一份内容）。
func fileSHA256(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// saveSF2Upload 流式接收、校验并落盘 SF2（S-M6）。
//
// 此前的实现把整个文件读进内存再写盘：200MB 音色库要在渲染进程
// （ArrayBuffer）与 Go（buf）各留一份副本，上限被卡在 256MB，500MB+
// 的专业音色库直接拒绝。现在边读边写临时文件，校验通过后原子改名，
// 内存占用与文件大小无关；桌面模式前端直接以 File 作为请求体流式上传。
//
// 校验三层：魔数（RIFF....sfbk）、RIFF 声明长度与实收字节一致（挡半截文件）、
// 大小上限（MaxBytesReader 超限与客户端中断都必须显式失败）。
//
// 返回落盘绝对路径与字节数。
func saveSF2Upload(req *http.Request, rawName string) (string, int64, *sf2UploadError) {
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(rawName)), ".sf3") {
		return "", 0, &sf2UploadError{http.StatusBadRequest, "不支持 SF3（Vorbis 压缩音色库）：请先转换为 SF2 再导入"}
	}
	if req.ContentLength > maxSF2Upload {
		return "", 0, &sf2UploadError{http.StatusRequestEntityTooLarge, "SF2 文件超过 4GB 上限"}
	}
	// w 参数仅用于向连接标记 requestTooLarge（类型断言失败即跳过），传 nil 即可
	req.Body = http.MaxBytesReader(nil, req.Body, maxSF2Upload)

	dir := filepath.Join(config.LibraryDir, "soundfonts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, &sf2UploadError{http.StatusInternalServerError, "创建音色目录失败：" + err.Error()}
	}
	tmp, err := os.CreateTemp(dir, ".upload-*.tmp")
	if err != nil {
		return "", 0, &sf2UploadError{http.StatusInternalServerError, "创建临时文件失败：" + err.Error()}
	}
	tmpName := tmp.Name()
	fail := func(e *sf2UploadError) (string, int64, *sf2UploadError) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", 0, e
	}

	var header [12]byte
	if _, err := io.ReadFull(req.Body, header[:]); err != nil {
		return fail(&sf2UploadError{http.StatusBadRequest, "接收上传数据失败，请重试"})
	}
	// SF2 魔数校验：RIFF....sfbk
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "sfbk" {
		return fail(&sf2UploadError{http.StatusBadRequest, "不是有效的 SF2 文件"})
	}
	if _, err := tmp.Write(header[:]); err != nil {
		return fail(&sf2UploadError{http.StatusInternalServerError, "写入音色文件失败"})
	}
	n, err := io.Copy(tmp, req.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return fail(&sf2UploadError{http.StatusRequestEntityTooLarge, "SF2 文件超过 4GB 上限"})
		}
		return fail(&sf2UploadError{http.StatusBadRequest, "接收上传数据失败，请重试"})
	}
	total := n + int64(len(header))
	// RIFF size 一致性校验：header[4:8] 为小端块长度（不含头部 8 字节）
	declared := uint32(header[4]) | uint32(header[5])<<8 | uint32(header[6])<<16 | uint32(header[7])<<24
	if int64(declared)+8 != total {
		return fail(&sf2UploadError{http.StatusBadRequest, "SF2 文件不完整（RIFF size 与实收数据不符）"})
	}
	if err := tmp.Sync(); err != nil {
		return fail(&sf2UploadError{http.StatusInternalServerError, "写入音色文件失败"})
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", 0, &sf2UploadError{http.StatusInternalServerError, "写入音色文件失败"}
	}
	newHash, herr := fileSHA256(tmpName)
	if herr != nil {
		_ = os.Remove(tmpName)
		return "", 0, &sf2UploadError{http.StatusInternalServerError, "校验音色文件失败"}
	}

	base := filepath.Base(strings.TrimSpace(rawName))
	if base == "" || base == "." {
		base = "uploaded.sf2"
	}
	base = strings.TrimSuffix(base, filepath.Ext(base))
	safe := sf2NameRe.ReplaceAllString(base, "_")
	if strings.TrimSpace(safe) == "" {
		safe = "soundfont"
	}
	dest := filepath.Join(dir, safe+".sf2")
	if st, serr := os.Stat(dest); serr == nil {
		same := st.Size() == total
		if same {
			oldHash, herr := fileSHA256(dest)
			same = herr == nil && oldHash == newHash
		}
		if same {
			// 同一份内容重复导入：沿用已有文件，不覆盖
			_ = os.Remove(tmpName)
			return dest, total, nil
		}
		/* S-M5：同名但内容不同 → 唯一命名，旧工程引用的旧文件保持不变 */
		dest = ""
		for i := 2; i < 100; i++ {
			candidate := filepath.Join(dir, safe+" ("+strconv.Itoa(i)+").sf2")
			cst, cerr := os.Stat(candidate)
			if os.IsNotExist(cerr) {
				dest = candidate
				break
			}
			if cerr == nil && cst.Size() == total {
				if h, herr := fileSHA256(candidate); herr == nil && h == newHash {
					dest = candidate
					break
				}
			}
		}
		if dest == "" {
			_ = os.Remove(tmpName)
			return "", 0, &sf2UploadError{http.StatusInternalServerError, "同名音色文件过多，请先整理音色目录"}
		}
	} else if !os.IsNotExist(serr) {
		_ = os.Remove(tmpName)
		return "", 0, &sf2UploadError{http.StatusInternalServerError, "检查目标文件失败：" + serr.Error()}
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return "", 0, &sf2UploadError{http.StatusInternalServerError, "提交音色文件失败：" + err.Error()}
	}
	return dest, total, nil
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

// sf2DisplayNameFor 展示名：保留用户原名；同名内容不同被落到 "名称 (2).sf2"
// 时把唯一后缀也带进展示名，界面里两条音源才能区分。
func sf2DisplayNameFor(rawName, dest string) string {
	display := sf2DisplayName(rawName)
	if display == "" {
		return sf2DisplayNameFor(rawName, dest)
	}
	rawBase := filepath.Base(strings.TrimSpace(rawName))
	safe := sf2NameRe.ReplaceAllString(strings.TrimSuffix(rawBase, filepath.Ext(rawBase)), "_")
	if strings.TrimSpace(safe) == "" {
		return display
	}
	stored := strings.TrimSuffix(filepath.Base(dest), ".sf2")
	if strings.HasPrefix(stored, safe) {
		return display + strings.TrimPrefix(stored, safe)
	}
	return display
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

	rawName := req.URL.Query().Get("name")
	dest, size, uerr := saveSF2Upload(req, rawName)
	if uerr != nil {
		writeError(w, uerr.status, uerr.detail)
		return
	}

	result := map[string]any{"saved": dest, "bytes": size}
	if src, created, err := soundlib.EnsureSF2Source(dest, sf2DisplayNameFor(rawName, dest)); err == nil {
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
