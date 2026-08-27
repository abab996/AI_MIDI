package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"aimidi/internal/config"
	"aimidi/internal/engine"
	"aimidi/internal/midi"
)

// handleAudioSub /api/audio/* 子路由：引擎状态、设备、设置、测试音
func (r *Router) handleAudioSub(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/api/audio/status":
		r.handleAudioStatus(w, req)
	case "/api/audio/devices":
		r.handleAudioDevices(w, req)
	case "/api/audio/settings":
		if req.Method == http.MethodPost {
			r.handleAudioSettingsPost(w, req)
		} else {
			r.handleAudioSettingsGet(w, req)
		}
	case "/api/audio/test-tone":
		r.handleTestTone(w, req)
	case "/api/audio/soundfonts":
		r.handleAudioSoundfonts(w, req)
	case "/api/audio/control-panel":
		r.handleAudioControlPanel(w, req)
	case "/api/audio/selftest-result":
		r.handleSelftestResult(w, req)
	case "/api/audio/bounce":
		r.handleAudioBounce(w, req)
	case "/api/audio/bounce/file":
		r.handleAudioBounceFile(w, req)
	default:
		writeError(w, http.StatusNotFound, "unknown audio endpoint")
	}
}

// handleAudioStatus 引擎进程状态
func (r *Router) handleAudioStatus(w http.ResponseWriter, req *http.Request) {
	sup := engine.Get()
	if sup == nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": engine.StateStopped})
		return
	}
	st := sup.Status()
	if st.State == engine.StateReady {
		if raw, err := sup.RequestRaw("currentSummary", 5*time.Second); err == nil {
			var extra struct {
				Summary string `json:"summary"`
			}
			if json.Unmarshal(raw, &extra) == nil && extra.Summary != "" {
				st.DeviceSummary = extra.Summary
			}
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleAudioDevices 设备列表（透传引擎枚举结果）
func (r *Router) handleAudioDevices(w http.ResponseWriter, req *http.Request) {
	sup := engine.Get()
	if sup == nil {
		writeError(w, http.StatusServiceUnavailable, "音频引擎未启用")
		return
	}
	dl, err := sup.ListDevices()
	if err != nil {
		writeError(w, http.StatusBadGateway, "设备枚举失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dl)
}

// handleAudioSettingsGet 读取音频设置
func (r *Router) handleAudioSettingsGet(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, config.LoadSettings().Audio)
}

// handleAudioSettingsPost 更新音频设置并下发引擎
func (r *Router) handleAudioSettingsPost(w http.ResponseWriter, req *http.Request) {
	var body struct {
		EngineEnabled *bool   `json:"engine_enabled"`
		Driver        *string `json:"driver"`
		Device        *string `json:"device"`
		SampleRate    *int    `json:"sample_rate"`
		BufferSize    *int    `json:"buffer_size"`
		Backend       *string `json:"backend"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}

	s := config.LoadSettings()
	if body.EngineEnabled != nil {
		s.Audio.EngineEnabled = *body.EngineEnabled
	}
	if body.Driver != nil {
		s.Audio.Driver = *body.Driver
	}
	if body.Device != nil {
		s.Audio.Device = *body.Device
	}
	if body.SampleRate != nil {
		if *body.SampleRate < 0 || *body.SampleRate > 384000 {
			writeError(w, http.StatusBadRequest, "sample_rate 超出范围")
			return
		}
		s.Audio.SampleRate = *body.SampleRate
	}
	if body.BufferSize != nil {
		if *body.BufferSize < 0 || *body.BufferSize > 8192 {
			writeError(w, http.StatusBadRequest, "buffer_size 超出范围")
			return
		}
		s.Audio.BufferSize = *body.BufferSize
	}
	if body.Backend != nil {
		b := *body.Backend
		// 兼容前端旧值 webaudio/auto
		switch b {
		case "webaudio", "auto":
			s.Audio.Backend = b
		default:
			writeError(w, http.StatusBadRequest, "backend 仅支持 auto/webaudio")
			return
		}
	}
	if s.Audio.Backend == "" {
		s.Audio.Backend = "auto"
	}

	if err := config.SaveSettings(s); err != nil {
		writeError(w, http.StatusInternalServerError, "保存设置失败: "+err.Error())
		return
	}

	// 引擎启用状态变化需要重建守护器，M1 提示重启主程序生效
	sup := engine.Get()
	restartRequired := sup != nil && sup.StartedWithEnabled() != s.Audio.EngineEnabled
	if !restartRequired && sup != nil {
		if err := sup.ApplySettings(s.Audio); err != nil {
			writeError(w, http.StatusBadGateway, "设置已保存，但下发引擎失败: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"saved":            true,
		"audio":            s.Audio,
		"restart_required": restartRequired,
	})
}

// handleTestTone 测试音开关
func (r *Router) handleTestTone(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var body struct {
		On   bool     `json:"on"`
		Freq *float64 `json:"freq"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	freq := 440.0
	if body.Freq != nil {
		freq = *body.Freq
	}
	sup := engine.Get()
	if sup == nil {
		writeError(w, http.StatusServiceUnavailable, "音频引擎未启用")
		return
	}
	if err := sup.TestTone(body.On, freq); err != nil {
		writeError(w, http.StatusBadGateway, "测试音下发失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"on": body.On, "freq": freq})
}

// handleAudioControlPanel 打开声卡驱动控制面板（ASIO 专用）
func (r *Router) handleAudioControlPanel(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	sup := engine.Get()
	if sup == nil {
		writeError(w, http.StatusServiceUnavailable, "音频引擎未就绪")
		return
	}
	opened, err := sup.OpenControlPanel()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "打开控制面板失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"opened": opened})
}

// handleAudioBounce 离线 bounce（按全局采样率，尾音播到静默不截断）
func (r *Router) handleAudioBounce(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, 5<<20) // 5MB 上限，防超大 tracks
	sup := engine.Get()
	if sup == nil {
		writeError(w, http.StatusServiceUnavailable, "音频引擎未启用")
		return
	}
	var body struct {
		Bpm       *float64 `json:"bpm"`
		Beats     *float64 `json:"beats"`
		TailSec   *float64 `json:"tailSec"`
		Tracks    any      `json:"tracks"`
		SampleRate *int    `json:"sampleRate"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	settings := config.LoadSettings()
	bpm := 120.0
	if body.Bpm != nil && *body.Bpm > 20 {
		bpm = *body.Bpm
	} else if settings.Audio.SampleRate > 0 {
		// 尝试从已有工程取 bpm，若无则 120
	}
	sampleRate := settings.Audio.SampleRate
	if body.SampleRate != nil && *body.SampleRate > 8000 {
		sampleRate = *body.SampleRate
	}
	if sampleRate <= 0 {
		sampleRate = 44100
	}
	beats := 16.0
	if body.Beats != nil && *body.Beats > 0 {
		beats = *body.Beats
	}
	tailSec := 2.5
	if body.TailSec != nil && *body.TailSec >= 0 {
		tailSec = *body.TailSec
	}
	// 若提供了 tracks，尝试从中推导最大拍（取 clips/midi 的最远 end，含 clip内 notes）
	if body.Tracks != nil {
		if tracks, ok := body.Tracks.([]any); ok {
			maxEnd := beats
			for _, t := range tracks {
				if tm, ok := t.(map[string]any); ok {
					if clips, ok := tm["clips"].([]any); ok {
						for _, c := range clips {
							if cm, ok := c.(map[string]any); ok {
								sv, _ := cm["start"].(float64)
								lv, _ := cm["length"].(float64)
								if e := sv + lv; e > maxEnd {
									maxEnd = e
								}
								// midi clip 内嵌 notes
								if notes, ok := cm["notes"].([]any); ok {
									for _, n := range notes {
										if nm, ok := n.(map[string]any); ok {
											ns, _ := nm["start"].(float64)
											ne, _ := nm["end"].(float64)
											clipStart, _ := cm["start"].(float64)
											absEnd := clipStart + ne
											absStart := clipStart + ns
											_ = absStart
											if absEnd > maxEnd {
												maxEnd = absEnd
											}
										}
									}
								}
							}
						}
					}
				}
			}
			beats = maxEnd
		}
	}
	// 输出路径：output/bounce_<ts>.wav（全局生效采样率，尾音已含）
	ts := time.Now().Format("20060102_150405")
	outPath := filepath.Join("output", fmt.Sprintf("bounce_%s.wav", ts))
	_ = os.MkdirAll(filepath.Dir(outPath), 0755)
	abs, _ := filepath.Abs(outPath)
	params := map[string]any{
		"bpm":        bpm,
		"beats":      beats,
		"tailSec":    tailSec,
		"sampleRate": sampleRate,
		"path":       abs,
	}
	// 扁平化 tracks → notes/clips 供引擎离线渲染（真实尾音，不截断）
	var flatNotes []map[string]any
	var flatClips []map[string]any
	if tracks, ok := body.Tracks.([]any); ok {
		for ti, t := range tracks {
			if tm, ok := t.(map[string]any); ok {
				if clips, ok := tm["clips"].([]any); ok {
					for _, c := range clips {
						if cm, ok := c.(map[string]any); ok {
							typ, _ := cm["type"].(string)
							if typ == "midi" {
								if notes, ok := cm["notes"].([]any); ok {
									clipStart, _ := cm["start"].(float64)
									for _, n := range notes {
										if nm, ok := n.(map[string]any); ok {
											noteStr, _ := nm["note"].(string)
											midiNum := 60
											if noteStr != "" {
												if v, err := midi.NoteNameToMidiNumber(noteStr); err == nil {
													midiNum = v
												}
											}
											ns, _ := nm["start"].(float64)
											ne, _ := nm["end"].(float64)
											velF, _ := nm["velocity"].(float64)
											if velF == 0 {
												velF = 100
											}
											flatNotes = append(flatNotes, map[string]any{
												"track": ti,
												"key":   midiNum,
												"vel":   int(velF),
												"start": clipStart + ns,
												"end":   clipStart + ne,
											})
										}
									}
								}
							} else if typ == "audio" {
								if src, ok := cm["src"].(map[string]any); ok {
									if p, ok := src["p"].(string); ok && p != "" {
										sv, _ := cm["start"].(float64)
										lv, _ := cm["length"].(float64)
										off, _ := cm["offset"].(float64)
										fi, _ := cm["fadeIn"].(float64)
										fo, _ := cm["fadeOut"].(float64)
										gv, _ := cm["gain"].(float64)
										if gv == 0 {
											gv = 1
										}
										flatClips = append(flatClips, map[string]any{
											"track":  ti,
											"path":   p,
											"start":  sv,
											"length": lv,
											"offset": off,
											"fadeIn": fi,
											"fadeOut": fo,
											"gain":   gv,
										})
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if len(flatNotes) > 0 {
		params["notes"] = flatNotes
	}
	if len(flatClips) > 0 {
		params["clips"] = flatClips
	} else if body.Tracks != nil {
		params["tracks"] = body.Tracks
	}
	retPath, err := sup.Bounce(params)
	if err != nil {
		writeError(w, http.StatusBadGateway, "离线渲染失败: "+err.Error())
		return
	}
	if retPath == "" {
		retPath = abs
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"path": retPath,
		"url":  "/api/audio/bounce/file?path=" + retPath,
		"beats": beats,
		"tailSec": tailSec,
		"sampleRate": sampleRate,
	})
}

// handleAudioBounceFile 下载已渲染的 WAV
func (r *Router) handleAudioBounceFile(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "缺少 path")
		return
	}
	// 仅允许 output 目录下的文件（此前 cwd 整目录放行会让
	// ?path=settings.json 回读含明文 API Key 的配置，绕过设置接口的掩码）
	abs, _ := filepath.Abs(path)
	outDir, _ := filepath.Abs("output")
	if !isSubPath(abs, outDir) {
		writeError(w, http.StatusForbidden, "非法路径")
		return
	}
	if _, err := os.Stat(abs); err != nil {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(abs)+"\"")
	http.ServeFile(w, req, abs)
}

func isSubPath(target, base string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return len(rel) > 0 && rel[0] != '.' && !filepath.IsAbs(rel)
}
