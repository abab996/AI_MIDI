package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aimidi/internal/config"
	"aimidi/internal/llm"
	"aimidi/internal/midi"
)

const (
	FuncAddChord  = "配和弦"
	FuncTranslate = "翻译歌词"
	FuncMelisma   = "设计转音"
	FuncOther     = "其他要求"
)

func (r *Router) handleParseMIDI(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	err := req.ParseMultipartForm(32 << 20) // 32MB max
	if err != nil {
		writeErr(w, http.StatusBadRequest, "解析表单数据失败", err)
		return
	}

	file, header, err := req.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "缺少上传文件", err)
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".mid" && ext != ".midi" {
		writeError(w, http.StatusBadRequest, "仅支持 .mid / .midi 文件")
		return
	}

	_ = os.MkdirAll(filepath.Dir(config.InputMidi), 0755)
	dst, err := os.Create(config.InputMidi)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存上传文件失败", err)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入上传文件失败", err)
		return
	}

	noteTable, err := midi.GetNote(config.InputMidi, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "解析失败，请检查 MIDI 文件后重试。")
		return
	}
	if len(noteTable) == 0 {
		writeError(w, http.StatusBadRequest, "未解析出音符，请检查 MIDI 文件是否有效。")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     fmt.Sprintf("✓ 已解析 %d 个音符", len(noteTable)),
		"note_count": len(noteTable),
		"note_table": noteTable,
	})
}

func (r *Router) handleRunTask(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var in struct {
		Func             string   `json:"func"`
		NoteTable        []string `json:"note_table"`
		BPM              string   `json:"bpm"`
		TimeSignature    string   `json:"time_signature"`
		Lyrics           string   `json:"lyrics"`
		OriginalLanguage string   `json:"original_language"`
		TargetLanguage   string   `json:"target_language"`
		NoteOutput       bool     `json:"note_output"`
		Requirements     string   `json:"requirements"`
	}

	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
		return
	}

	flusher := setupSSEHeaders(w)
	startTime := time.Now()

	elapsed := func(msg string) string {
		return fmt.Sprintf("%s（耗时 %.2f 秒）", msg, time.Since(startTime).Seconds())
	}

	sendSSE := func(data map[string]any) {
		_, _ = fmt.Fprint(w, sseEvent(data))
		if flusher != nil {
			flusher.Flush()
		}
	}

	if in.Func != FuncOther && len(in.NoteTable) == 0 {
		sendSSE(map[string]any{"type": "error", "message": elapsed("⚠ 请先解析 MIDI 文件。")})
		return
	}

	if in.Func == FuncOther {
		sendSSE(map[string]any{"type": "progress", "value": 0.2, "desc": "准备请求"})
	} else {
		sendSSE(map[string]any{"type": "progress", "value": 0.2, "desc": "解析 MIDI"})
	}

	settings := config.LoadSettings()
	if settings.APIKey == "" {
		sendSSE(map[string]any{"type": "error", "message": elapsed("⚠ 请先在设置页填写并保存 API Key。")})
		return
	}

	bpmInt, err := strconv.Atoi(strings.TrimSpace(in.BPM))
	if err != nil {
		bpmInt = config.DefaultBPM
	}
	bpm := strconv.Itoa(midi.ClampBPM(bpmInt))

	timeSig := strings.TrimSpace(in.TimeSignature)
	if timeSig == "" {
		timeSig = config.DefaultTimeSignature
	}

	noteText := strings.Join(in.NoteTable, "\n")

	sendSSE(map[string]any{"type": "progress", "value": 0.5, "desc": "调用 AI"})

	var result string
	switch in.Func {
	case FuncAddChord:
		result, err = llm.AddChord(noteText, bpm, timeSig, in.Requirements, settings)
	case FuncTranslate:
		if strings.TrimSpace(in.OriginalLanguage) == "" || strings.TrimSpace(in.TargetLanguage) == "" {
			sendSSE(map[string]any{"type": "error", "message": elapsed("⚠ 请填写原语言和目标语言。")})
			return
		}
		result, err = llm.TranslateLyrics(noteText, in.Lyrics, bpm, timeSig, in.OriginalLanguage, in.TargetLanguage, settings)
	case FuncMelisma:
		result, err = llm.DesignMelisma(noteText, in.Lyrics, bpm, timeSig, in.Requirements, settings)
	default: // FuncOther
		if strings.TrimSpace(in.Requirements) == "" {
			sendSSE(map[string]any{"type": "error", "message": elapsed("⚠ 请填写具体要求。")})
			return
		}
		result, err = llm.OtherRequirements(noteText, in.Lyrics, bpm, timeSig, in.Requirements, in.NoteOutput, settings)
	}

	if err != nil || result == "" {
		/* llm 层返回的错误本就是面向用户的中文原因（API Key 缺失/网络/状态码），
		   透传给用户而非吞掉；空结果单独留痕便于区分"模型回了空串" */
		slog.Error("LLM 调用失败", "func", in.Func, "err", err, "result_len", len(result))
		msg := "✗ 调用失败，请稍后重试。"
		if err != nil {
			msg = "✗ 调用失败：" + err.Error()
		}
		sendSSE(map[string]any{"type": "error", "message": elapsed(msg)})
		return
	}

	sendSSE(map[string]any{"type": "progress", "value": 0.9, "desc": "保存结果"})
	_ = os.MkdirAll(config.OutputDir, 0755)

	var downloadPath string
	var statusMsg string

	if in.Func == FuncTranslate {
		savePath := filepath.Join(config.OutputDir, "translated_lyrics.txt")
		_ = os.WriteFile(savePath, []byte(result), 0644)
		downloadPath = savePath
		statusMsg = "✓ 歌词已保存"
	} else if in.Func == FuncOther && !in.NoteOutput {
		savePath := filepath.Join(config.OutputDir, "other_requirements_result.txt")
		_ = os.WriteFile(savePath, []byte(result), 0644)
		downloadPath = savePath
		statusMsg = "✓ 结果已保存"
	} else {
		err := midi.OutNote(result, bpmInt, config.OutputMidi)
		if err != nil {
			if err == midi.ErrEmptyNoteTable {
				sendSSE(map[string]any{
					"type":         "done",
					"result":       result,
					"download_url": nil,
					"status":       elapsed("⚠ AI 回复中未解析出有效音符，请检查 AI 输出格式。"),
				})
				return
			}
			sendSSE(map[string]any{
				"type":         "done",
				"result":       result,
				"download_url": nil,
				"status":       elapsed("✓ AI 返回结果，但保存失败，请重试。"),
			})
			return
		}
		downloadPath = config.OutputMidi
		statusMsg = fmt.Sprintf("✓ MIDI 已生成: %s", filepath.Base(config.OutputMidi))
	}

	rel, _ := filepath.Rel(config.ProjectRoot, downloadPath)
	downloadURL := "/api/files/download?path=" + url.QueryEscape(filepath.ToSlash(rel))

	sendSSE(map[string]any{
		"type":         "done",
		"result":       result,
		"download_url": downloadURL,
		"status":       elapsed(statusMsg),
	})
}

func (r *Router) handleDownloadFile(w http.ResponseWriter, req *http.Request) {
	relPath := req.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "缺少 path 参数")
		return
	}

	cleanRel := filepath.Clean(filepath.FromSlash(relPath))
	target := filepath.Join(config.ProjectRoot, cleanRel)
	targetClean := filepath.Clean(target)

	allowedRoots := []string{
		filepath.Clean(config.OutputDir),
		filepath.Clean(config.DoingDir),
		filepath.Clean(config.ProjectsDir),
	}

	isAllowed := false
	for _, root := range allowedRoots {
		rel, err := filepath.Rel(root, targetClean)
		if err == nil && !strings.HasPrefix(rel, "..") {
			isAllowed = true
			break
		}
	}

	if !isAllowed {
		writeError(w, http.StatusForbidden, "路径不在允许范围内")
		return
	}

	fi, err := os.Stat(targetClean)
	if err != nil || fi.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fi.Name()))
	http.ServeFile(w, req, targetClean)
}
