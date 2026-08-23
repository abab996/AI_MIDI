package server

import (
	"encoding/json"
	"net/http"

	"aimidi/internal/config"
	"aimidi/internal/engine"
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
	writeJSON(w, http.StatusOK, sup.Status())
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
