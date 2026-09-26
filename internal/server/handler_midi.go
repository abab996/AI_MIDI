package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"aimidi/internal/config"
	"aimidi/internal/llm"
	"aimidi/internal/midi"
	"aimidi/internal/tasks"
)

const (
	FuncAddChord  = "配和弦"
	FuncTranslate = "翻译歌词"
	FuncMelisma   = "设计转音"
	FuncOther     = "其他要求"

	// quickTaskProject /api/run 快捷任务在任务注册表中挂靠的伪项目：
	// 不属于任何真实档案库，因此不会出现在项目任务列表里
	quickTaskProject = "__quicktask__"
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
		slog.Warn("上传 MIDI 解析失败", "file", header.Filename, "err", err)
		writeError(w, http.StatusBadRequest, "解析失败，请检查 MIDI 文件后重试。")
		return
	}
	if len(noteTable) == 0 {
		writeError(w, http.StatusBadRequest, "未解析出音符，请检查 MIDI 文件是否有效。")
		return
	}

	// 解析成功才留历史副本：input/in.mid 是固定单槽，下一次上传会覆盖，
	// 历史副本让上一次的文件仍可找回（只保留最近 10 份）
	backupInputHistory()

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

	// sendSSE 带写超时：/api/run 未设连接级 WriteTimeout（SSE 不能掐断），
	// 若无 per-write deadline，客户端停止读取但未关闭（后台标签页节流/
	// 僵死连接）时 Fprint 会无限阻塞，handler goroutine 与连接永久泄漏。
	// 与 /api/chat 的 sseCallback（15s deadline）同一手法。
	rc := http.NewResponseController(w)
	sendSSE := func(data map[string]any) {
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
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

	settings, resolveErr := config.ResolveCall(config.LoadSettings())
	if resolveErr != nil {
		sendSSE(map[string]any{"type": "error", "message": elapsed(resolveErr.Error())})
		return
	}
	// 本地服务（Ollama / LM Studio / vLLM / llama.cpp）不校验密钥，别拦
	if settings.APIKey == "" && config.RequiresAPIKey(settings.BaseURL) {
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

	// 注册进任务注册表：/api/run 从此可被 /api/tasks/{id}/stop 停止，
	// 与对话任务共用同一条停止链路。runCtx 独立于 req.Context()——
	// 断开 SSE（关页面）不算取消，只有显式点停止才中止
	rec := tasks.TaskEnsure(quickTaskProject, nil, strings.TrimSpace(in.Func)+" "+strings.TrimSpace(in.Requirements))
	tid := rec.ID
	tasks.TaskMarkRunning(tid)
	defer tasks.TaskFinish(tid)

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if ch := tasks.TaskCancelChan(tid); ch != nil {
		go func() {
			select {
			case <-ch:
				cancelRun()
			case <-runCtx.Done():
			}
		}()
	}

	sendSSE(map[string]any{"type": "progress", "value": 0.5, "desc": "调用 AI", "task_id": tid})

	var result string
	switch in.Func {
	case FuncAddChord:
		result, err = llm.AddChord(runCtx, noteText, bpm, timeSig, in.Requirements, settings)
	case FuncTranslate:
		if strings.TrimSpace(in.OriginalLanguage) == "" || strings.TrimSpace(in.TargetLanguage) == "" {
			sendSSE(map[string]any{"type": "error", "message": elapsed("⚠ 请填写原语言和目标语言。")})
			sendSSE(map[string]any{"type": "done"})
			return
		}
		result, err = llm.TranslateLyrics(runCtx, noteText, in.Lyrics, bpm, timeSig, in.OriginalLanguage, in.TargetLanguage, settings)
	case FuncMelisma:
		result, err = llm.DesignMelisma(runCtx, noteText, in.Lyrics, bpm, timeSig, in.Requirements, settings)
	default: // FuncOther
		if strings.TrimSpace(in.Requirements) == "" {
			sendSSE(map[string]any{"type": "error", "message": elapsed("⚠ 请填写具体要求。")})
			sendSSE(map[string]any{"type": "done"})
			return
		}
		result, err = llm.OtherRequirements(runCtx, noteText, in.Lyrics, bpm, timeSig, in.Requirements, in.NoteOutput, settings)
	}

	if err != nil || result == "" {
		slog.Error("LLM 调用失败", "func", in.Func, "err", err, "result_len", len(result))
		if errors.Is(runCtx.Err(), context.Canceled) || tasks.TaskIsCancelled(tid) {
			sendSSE(map[string]any{"type": "error", "message": elapsed("已停止")})
			sendSSE(map[string]any{"type": "done"})
			return
		}
		var ev map[string]any
		var ue *llm.UpstreamError
		if errors.As(err, &ue) {
			ev = map[string]any{"type": "error", "code": ue.Code, "message": elapsed("✗ " + ue.Message)}
			if ue.Detail != "" {
				ev["detail"] = ue.Detail
			}
		} else if err != nil {
			ev = map[string]any{"type": "error", "message": elapsed("✗ 调用失败：" + err.Error())}
		} else {
			ev = map[string]any{"type": "error", "message": elapsed("✗ 调用失败：AI 返回了空结果，请重试。")}
		}
		sendSSE(ev)
		sendSSE(map[string]any{"type": "done"})
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
		// 输出改用时间戳文件名：固定 output.mid 会让上一次成果被静默
		// 覆盖（前一次的下载链接指向新文件）。文件面板按目录列举展示，
		// 历史产物仍可从列表找回
		savePath := uniqueRunPath()
		err := midi.OutNote(result, bpmInt, savePath)
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
		downloadPath = savePath
		statusMsg = fmt.Sprintf("✓ MIDI 已生成: %s", filepath.Base(savePath))
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

// uniqueRunPath 生成不冲突的快捷任务输出路径 output/run-<时间戳>.mid；
// 同秒多次生成时追加序号后缀
func uniqueRunPath() string {
	base := time.Now().Format("20060102-150405")
	p := filepath.Join(config.OutputDir, "run-"+base+".mid")
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(config.OutputDir, fmt.Sprintf("run-%s-%d.mid", base, i))
	}
}

// backupInputHistory 把刚上传解析成功的 in.mid 复制到 input/history/，
// 只保留最近 maxInputBackups 份（input/ 目录整体不入库）
func backupInputHistory() {
	const maxInputBackups = 10
	histDir := filepath.Join(filepath.Dir(config.InputMidi), "history")
	if err := os.MkdirAll(histDir, 0755); err != nil {
		return
	}
	if data, err := os.ReadFile(config.InputMidi); err == nil {
		dst := filepath.Join(histDir, "in-"+time.Now().Format("20060102-150405")+".mid")
		_ = os.WriteFile(dst, data, 0644)
	}
	entries, err := os.ReadDir(histDir)
	if err != nil {
		return
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "in-") && strings.HasSuffix(e.Name(), ".mid") {
			backups = append(backups, filepath.Join(histDir, e.Name()))
		}
	}
	// 时间戳文件名按字典序即时间序，超出保留数从最旧开始删
	sort.Strings(backups)
	for i := 0; i < len(backups)-maxInputBackups; i++ {
		_ = os.Remove(backups[i])
	}
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
