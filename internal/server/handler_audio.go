package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	case "/api/audio/panic":
		r.handleAudioPanic(w, req)
	default:
		writeError(w, http.StatusNotFound, "unknown audio endpoint")
	}
}

// handleAudioPanic 全音符停止（卡音逃生口：丢 note-off、音色热切换、
// 后端切换都可能留下响个不停的原生音符）
func (r *Router) handleAudioPanic(w http.ResponseWriter, req *http.Request) {
	sup := engine.Get()
	if sup == nil {
		writeError(w, http.StatusServiceUnavailable, "音频引擎未启用")
		return
	}
	if err := sup.PanicAll(); err != nil {
		writeError(w, http.StatusBadGateway, "panic 失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
		// 保存前对照引擎实际枚举校验驱动/设备名：此前无效设备名会被
		// 静默保存，重放每次失败只写日志，UI 表现为"改了什么都没反应"
		if err := validateDeviceSelection(sup, s.Audio); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		attemptedDriver := s.Audio.Driver
		attemptedDevice := s.Audio.Device
		if err := sup.ApplySettings(s.Audio); err != nil {
			// 超时（驱动卡死）或业务失败（设备不存在）：ApplySettings 已把
			// 内存设置回滚到最近可用配置，这里统一持久化之，保持 settings.json
			// 与实际设备一致
			s.Audio = sup.CurrentAudio()
			_ = config.SaveSettings(s)
			name := attemptedDevice
			if name == "" {
				name = attemptedDriver
			}
			if isEngineTimeoutErr(err) {
				writeError(w, http.StatusGatewayTimeout, name+" 未响应（已回退到上次可用设备）")
				return
			}
			writeError(w, http.StatusBadGateway, "设置未生效（已回退到上次可用设备）: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"saved":            true,
		"audio":            s.Audio,
		"restart_required": restartRequired,
	})
}

// isEngineTimeoutErr 判定错误是否为引擎请求超时（读取/写入/等待超时）。
// 驱动在引擎内卡死时的典型表现即此类错误。
func isEngineTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "超时") || strings.Contains(msg, "timeout")
}

// validateDeviceSelection 保存音频设置前对照引擎实际枚举校验驱动/设备名，
// 与引擎 applySetup 的精确匹配语义一致。无效组合立即 400 并列出可用项，
// 不再落盘（此前只写日志，UI 表现为"改了什么都没反应"）。
// 引擎未就绪或枚举失败时跳过——尽力而为，不阻塞保存
func validateDeviceSelection(sup *engine.Supervisor, a engine.AudioSettings) error {
	if a.Driver == "" && a.Device == "" {
		return nil
	}
	dl, err := sup.ListDevices()
	if err != nil || dl == nil {
		return nil
	}
	if a.Driver != "" {
		found := false
		for _, dt := range dl.Drivers {
			if dt.Driver == a.Driver {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("驱动类型 %q 不存在（引擎可用: %s），请从设备下拉中选择",
				a.Driver, joinDriverNames(dl))
		}
	}
	if a.Device != "" {
		for _, dt := range dl.Drivers {
			if a.Driver != "" && dt.Driver != a.Driver {
				continue
			}
			for _, name := range dt.Devices {
				if name == a.Device {
					return nil
				}
			}
		}
		return fmt.Errorf("设备 %q 不在引擎枚举列表中，请从设备下拉中选择实际存在的设备（可在驱动面板确认安装状态）",
			a.Device)
	}
	return nil
}

// joinDriverNames 列出引擎可用的驱动类型名（提示用）
func joinDriverNames(dl *engine.DeviceList) string {
	names := make([]string, 0, len(dl.Drivers))
	for _, dt := range dl.Drivers {
		names = append(names, dt.Driver)
	}
	if len(names) == 0 {
		return "无"
	}
	return strings.Join(names, " / ")
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
		Bpm        *float64 `json:"bpm"`
		Beats      *float64 `json:"beats"`
		TailSec    *float64 `json:"tailSec"`
		Tracks     any      `json:"tracks"`
		SampleRate *int     `json:"sampleRate"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	settings := config.LoadSettings()
	bpm := 120.0
	if body.Bpm != nil && *body.Bpm > 20 {
		bpm = *body.Bpm
	}
	// 采样率语义说明：此处复用「设备采样率」设置作为导出渲染率。
	// 两者语义不同（设备可能跑 44.1kHz 而导出想要 48kHz），当前产品
	// 未区分——显式传参 sampleRate 可覆盖。详见 docs/audit-audio-update-2026-09-05.md
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
	// 输出路径：output/bounce_<ts>.wav（全局生效采样率，尾音已含）。
	// 固定用 config.OutputDir（exe 目录），与下载白名单一致；避免从
	// 其他工作目录启动时文件落到预期外位置
	ts := time.Now().Format("20060102_150405")
	outPath := filepath.Join(config.OutputDir, fmt.Sprintf("bounce_%s.wav", ts))
	_ = os.MkdirAll(filepath.Dir(outPath), 0755)
	abs := outPath
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
											"track":   ti,
											"path":    p,
											"start":   sv,
											"length":  lv,
											"offset":  off,
											"fadeIn":  fi,
											"fadeOut": fo,
											"gain":    gv,
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
		"ok":         true,
		"path":       retPath,
		"url":        "/api/audio/bounce/file?path=" + url.QueryEscape(retPath),
		"beats":      beats,
		"tailSec":    tailSec,
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
	outDir, _ := filepath.Abs(config.OutputDir)
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
