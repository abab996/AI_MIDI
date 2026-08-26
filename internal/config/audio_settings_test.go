package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAudioBackendDefault(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "aimidi_audio_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	orig := SettingsFile
	SettingsFile = filepath.Join(tmpDir, "settings.json")
	defer func() { SettingsFile = orig; settingsCache = nil }()

	// 首次加载无文件时应为 auto
	s := LoadSettings()
	if s.Audio.Backend != "auto" {
		t.Fatalf("default backend = %q, want auto", s.Audio.Backend)
	}
	if !s.Audio.EngineEnabled {
		t.Fatalf("default EngineEnabled should be true")
	}
}

func TestAudioBackendPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "aimidi_audio_test2_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	orig := SettingsFile
	SettingsFile = filepath.Join(tmpDir, "settings.json")
	defer func() { SettingsFile = orig; settingsCache = nil }()

	cases := []string{"auto", "webaudio"}
	for _, want := range cases {
		s := LoadSettings()
		s.Audio.Backend = want
		if err := SaveSettings(s); err != nil {
			t.Fatalf("SaveSettings %q failed: %v", want, err)
		}
		// 清缓存后重读
		settingsCache = nil
		got := LoadSettings()
		if got.Audio.Backend != want {
			t.Fatalf("persist backend = %q, want %q", got.Audio.Backend, want)
		}
	}

	// 非法值应回退为 auto
	raw := `{"audio":{"backend":"invalid"}}`
	if err := os.WriteFile(SettingsFile, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	settingsCache = nil
	got := LoadSettings()
	if got.Audio.Backend != "auto" {
		t.Fatalf("invalid backend should fallback to auto, got %q", got.Audio.Backend)
	}
}

func TestAudioSampleRateAndTail(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "aimidi_audio_test3_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	orig := SettingsFile
	SettingsFile = filepath.Join(tmpDir, "settings.json")
	defer func() { SettingsFile = orig; settingsCache = nil }()

	s := DefaultSettings()
	s.Audio.SampleRate = 48000
	s.Audio.BufferSize = 128
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	settingsCache = nil
	loaded := LoadSettings()
	if loaded.Audio.SampleRate != 48000 {
		t.Fatalf("SampleRate = %d, want 48000", loaded.Audio.SampleRate)
	}
	if loaded.Audio.BufferSize != 128 {
		t.Fatalf("BufferSize = %d, want 128", loaded.Audio.BufferSize)
	}
	// 延迟估算：128/48000*1000 ≈ 2.67ms
	latency := float64(loaded.Audio.BufferSize) / float64(loaded.Audio.SampleRate) * 1000
	if latency < 2.6 || latency > 2.8 {
		t.Fatalf("latency %.2f out of expected 2.67", latency)
	}
}
