package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontendAudioRouting(t *testing.T) {
	// 验证默认走 JUCE：audio_engine.js 的 isNativePreferred 应被使用
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "js", "arrangement", "audio_engine.js"))
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	if !strings.Contains(s, "isNativePreferred") {
		t.Fatal("audio_engine.js should check isNativePreferred for JUCE routing")
	}
	if !strings.Contains(s, "noteOnTrack") {
		t.Fatal("audio_engine.js should call EngineBridge.noteOnTrack for native")
	}
	// 验证 _queueAudioClip 在原生时跳过 Web 队列
	if !strings.Contains(s, "if (isNativePreferred()) return;") {
		t.Fatal("_queueAudioClip should early return when native")
	}
}

func TestFrontendPianorollRouting(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "js", "pianoroll", "pianoroll.js"))
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	if !strings.Contains(s, "isNativePreferred") {
		t.Fatal("pianoroll.js should check isNativePreferred")
	}
	if !strings.Contains(s, "noteOnTrack") {
		t.Fatal("pianoroll.js should use noteOnTrack for native")
	}
}

func TestFrontendEngineBridge(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "js", "engine", "engine_bridge.js"))
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	// 必须有 timecode 和 levels 轮询
	if !strings.Contains(s, "__engineTimecode") {
		t.Fatal("engine_bridge.js should have timecode polling")
	}
	if !strings.Contains(s, "__engineLevels") {
		t.Fatal("engine_bridge.js should have levels polling")
	}
	if !strings.Contains(s, "getLevels") {
		t.Fatal("engine_bridge.js should expose getLevels")
	}
	if !strings.Contains(s, "setLoop") {
		t.Fatal("engine_bridge.js should expose setLoop")
	}
}

func TestFrontendSettingsConsistency(t *testing.T) {
	// ASIO 按钮应与测试音一致，不应有深底硬编码
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "settings.html"))
	if err != nil {
		t.Skip(err.Error())
	}
	if bytes.Contains(data, []byte(`style="background:#2a3b4c`)) {
		t.Fatal("settings.html ASIO button should not have hardcoded dark background")
	}
	css, err := os.ReadFile(filepath.Join("..", "..", "frontend", "style.css"))
	if err == nil {
		// 不应有 #openPanelBtn 的深底覆盖（已统一）
		if bytes.Contains(css, []byte("#openPanelBtn")) && bytes.Contains(css, []byte("#2a3b4c")) {
			t.Fatal("style.css should not have #openPanelBtn dark override after unification")
		}
	}
}

func TestEngineMixerAndTransport(t *testing.T) {
	// 验证 Mixer 有压限，Transport 有循环
	mixerData, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "MixerGraph.h"))
	if err == nil {
		s := string(mixerData)
		if !strings.Contains(s, "Compressor") || !strings.Contains(s, "Limiter") {
			t.Fatal("MixerGraph.h should have Compressor/Limiter")
		}
		if !strings.Contains(s, "peakLevel") {
			t.Fatal("MixerGraph should compute peakLevel")
		}
	}
	transportData, err := os.ReadFile(filepath.Join("..", "..", "engine", "Source", "Audio", "Transport.h"))
	if err == nil {
		s := string(transportData)
		if !strings.Contains(s, "setLoop") {
			t.Fatal("Transport.h should have setLoop")
		}
		if !strings.Contains(s, "loopOn_") {
			t.Fatal("Transport should have loop state")
		}
	}
}
