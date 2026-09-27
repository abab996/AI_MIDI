package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 音频后端设置项已移除：固定走原生引擎（桌面）/ WebAudio（无引擎桥的
// 浏览器模式由前端判定），旧配置里的 backend 键被忽略且不再影响行为
func TestAudioBackendFieldRemoved(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "aimidi_audio_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	orig := SettingsFile
	SettingsFile = filepath.Join(tmpDir, "settings.json")
	defer func() { SettingsFile = orig; settingsCache = nil }()

	if !LoadSettings().Audio.EngineEnabled {
		t.Fatalf("default EngineEnabled should be true")
	}

	// 旧配置（含 backend）仍能加载，不再报错（未知键忽略）
	raw := `{"audio":{"engine_enabled":false,"backend":"webaudio"}}`
	if err := os.WriteFile(SettingsFile, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	settingsCache = nil
	got := LoadSettings()
	if got.Audio.EngineEnabled {
		t.Fatalf("engine_enabled=false 应被保留")
	}
	// 重新保存后 backend 键应从文件中消失（结构体已无该字段）
	if err := SaveSettings(got); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(SettingsFile)
	if bytes.Contains(data, []byte("backend")) {
		t.Fatalf("保存后不应再有 backend 键: %s", data)
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

// TestIsTempDirCoverage：go run 的 exe 可能落在 os.TempDir() 之下，
// 也可能落在 GOCACHE（%LOCALAPPDATA%\go-build\<hash>-d，位于 Temp 之外）。
// 两者都必须识别为开发临时区，否则 ProjectRoot 会解析到构建缓存目录，
// settings/Library/projects 全线错位（E2E 中实际发生过：第二次 go run
// 读到默认设置且知识库不可用）
func TestIsTempDirCoverage(t *testing.T) {
	if !isTempDir(os.TempDir()) {
		t.Fatal("os.TempDir() 及其子目录应识别为临时区")
	}
	if !isTempDir(filepath.Join(os.TempDir(), "go-build123", "b001", "exe")) {
		t.Fatal("Temp 下的 go run 子目录应识别为临时区")
	}
	if cache := buildCacheDir(); cache != "" {
		if !isTempDir(cache) {
			t.Fatalf("Go 构建缓存根应识别为临时区: %s", cache)
		}
		if !isTempDir(filepath.Join(cache, "97", "975ebe-deadbeef-d")) {
			t.Fatal("构建缓存内的 go run 目录应识别为临时区")
		}
	}
	// 真实安装目录（任意非临时路径）不得误判
	if isTempDir(`C:\Program Files\AI_MIDI`) {
		t.Fatal("常规安装目录不应识别为临时区")
	}
}
