package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot 返回仓库根目录（以本测试文件位置为锚，任何 CWD 下均可工作）
func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(thisFile))
}

func readRepoFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

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
		s := string(readRepoFile(t, c.file))
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
	data := readRepoFile(t, "frontend/settings.html")
	if bytes.Contains(data, []byte(`style="background:#2a3b4c`)) {
		t.Fatal("ASIO button should be unified, not hardcoded dark")
	}
	css := readRepoFile(t, "frontend/style.css")
	if bytes.Contains(css, []byte("#openPanelBtn")) && bytes.Contains(css, []byte("#2a3b4c")) {
		t.Fatal("style.css should not have dark override for #openPanelBtn after unification")
	}
}

func TestM4LatencyAndHotplug(t *testing.T) {
	s := string(readRepoFile(t, "frontend/settings.html"))
	if !strings.Contains(s, "updateLatency") || !strings.Contains(s, "buffer/sampleRate") {
		t.Fatal("settings.html should have latency calculation")
	}
	if !strings.Contains(s, "热插拔") && !strings.Contains(s, "hashDevices") {
		t.Fatal("settings.html should have hotplug polling")
	}
}
