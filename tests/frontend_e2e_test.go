package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllDefaultIsJUCE(t *testing.T) {
	// 验证所有默认路径都经 JUCE，而非 WebAudio
	checks := []struct {
		file    string
		mustContain    []string
		mustNotContain []string
	}{
		{
			file: "frontend/js/arrangement/audio_engine.js",
			mustContain: []string{"isNativePreferred", "noteOnTrack"},
			mustNotContain: []string{},
		},
		{
			file: "frontend/js/pianoroll/pianoroll.js",
			mustContain: []string{"isNativePreferred", "noteOnTrack"},
			mustNotContain: []string{},
		},
		{
			file: "frontend/js/engine/engine_bridge.js",
			mustContain: []string{"__engineTimecode", "__engineLevels", "getLevels", "setLoop"},
			mustNotContain: []string{},
		},
		{
			file: "internal/engine/supervisor.go",
			mustContain: []string{"HideWindow", "CREATE_NO_WINDOW"},
			mustNotContain: []string{},
		},
		{
			file: "main.go",
			mustContain: []string{"5000 * time.Millisecond", "engineSup.Status()", "StateReady"},
			mustNotContain: []string{"1400 * time.Millisecond"},
		},
	}
	for _, c := range checks {
		data, err := os.ReadFile(filepath.Join("..", c.file))
		if err != nil {
			// 从 tests 目录执行时，.. 是 D:\pyx\AI_MIDI-go
			// 尝试绝对路径
			data, err = os.ReadFile(filepath.Join("D:\\pyx\\AI_MIDI-go", c.file))
			if err != nil {
				t.Fatalf("read %s: %v", c.file, err)
			}
		}
		s := string(data)
		for _, want := range c.mustContain {
			if !strings.Contains(s, want) {
				t.Errorf("%s should contain %q", c.file, want)
			}
		}
		for _, notWant := range c.mustNotContain {
			if strings.Contains(s, notWant) {
				t.Errorf("%s should not contain %q", c.file, notWant)
			}
		}
	}
}

func TestSettingsButtonUnified(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "frontend", "settings.html"))
	if err != nil {
		data, err = os.ReadFile("D:\\pyx\\AI_MIDI-go\\frontend\\settings.html")
		if err != nil {
			t.Fatal(err)
		}
	}
	if bytes.Contains(data, []byte(`style="background:#2a3b4c`)) {
		t.Fatal("ASIO button should be unified, not hardcoded dark")
	}
	css, err := os.ReadFile(filepath.Join("..", "frontend", "style.css"))
	if err != nil {
		css, _ = os.ReadFile("D:\\pyx\\AI_MIDI-go\\frontend\\style.css")
	}
	if bytes.Contains(css, []byte("#openPanelBtn")) && bytes.Contains(css, []byte("#2a3b4c")) {
		t.Fatal("style.css should not have dark override for #openPanelBtn after unification")
	}
}

func TestM4LatencyAndHotplug(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "frontend", "settings.html"))
	if err != nil {
		data, _ = os.ReadFile("D:\\pyx\\AI_MIDI-go\\frontend\\settings.html")
	}
	s := string(data)
	if !strings.Contains(s, "updateLatency") || !strings.Contains(s, "buffer/sampleRate") {
		t.Fatal("settings.html should have latency calculation")
	}
	if !strings.Contains(s, "热插拔") && !strings.Contains(s, "hashDevices") {
		t.Fatal("settings.html should have hotplug polling")
	}
}
