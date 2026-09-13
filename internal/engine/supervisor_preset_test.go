package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SetTrackPreset 轨越界应在 Ready 之前被拒绝（与 LoadSoundFontTrack 同纪律：
// 越界 track 会被引擎静默钳到 0，入口直接拒绝避免音色加载到错误轨道）
func TestSetTrackPresetRejectsOutOfRange(t *testing.T) {
	sup := NewSupervisor(Config{}, AudioSettings{})
	err := sup.SetTrackPreset(32, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "track 超出范围") {
		t.Fatalf("track=32 应被拒绝，got %v", err)
	}
	err = sup.SetTrackPreset(-1, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "track 超出范围") {
		t.Fatalf("track=-1 应被拒绝，got %v", err)
	}
	// 引擎未启动时合法 track 报"未就绪"而非越界
	err = sup.SetTrackPreset(0, 0, 0)
	if err == nil || strings.Contains(err.Error(), "track 超出范围") {
		t.Fatalf("track=0 不应报越界，got %v", err)
	}
}

// 会话重放顺序：loadSoundFont → setTrackPreset → setTrackVoice（源码顺序断言）。
// loadSoundFont 会重置预设状态、setTrackVoice 会撤下 SF2——顺序颠倒
// 会导致预设/波形声部被覆盖（冷启动重放回归护栏）
func TestPresetReplayOrdering(t *testing.T) {
	data, err := os.ReadFile("supervisor.go")
	if err != nil {
		t.Skip(err.Error())
	}
	s := string(data)
	fontReplay := strings.Index(s, "重放波形声部")
	presetReplay := strings.Index(s, "重放预设选择")
	if presetReplay < 0 {
		t.Fatal("supervisor.go 缺少预设重放段（重放预设选择）")
	}
	if fontReplay < 0 || presetReplay > fontReplay {
		t.Fatal("预设重放必须在波形声部重放之前（loadSoundFont → setTrackPreset → setTrackVoice）")
	}
	if !strings.Contains(s, `cli.Request("setTrackPreset"`) {
		t.Fatal("supervisor.go 应包含 setTrackPreset 重放请求")
	}
	if !strings.Contains(s, "lastPresets") {
		t.Fatal("supervisor.go 应有 lastPresets 每轨预设记录")
	}
}

// /api/audio/status 的 default_soundfont：音色目录首个 SF2 绝对路径；
// 空目录/未配置返回空串（内置 piano/strings 的原生映射目标）
func TestStatusDefaultSoundfont(t *testing.T) {
	dir := t.TempDir()
	// 空目录：空串
	sup := NewSupervisor(Config{SoundFontDir: dir}, AudioSettings{})
	if got := sup.Status().DefaultSoundfont; got != "" {
		t.Fatalf("空目录应返回空串，got %q", got)
	}
	// 有 SF2：返回按文件名排序的首个绝对路径
	f1 := filepath.Join(dir, "b.sf2")
	f2 := filepath.Join(dir, "a.sf2")
	for _, f := range []string{f1, f2} {
		if err := os.WriteFile(f, []byte("fake"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sup2 := NewSupervisor(Config{SoundFontDir: dir}, AudioSettings{})
	if got := sup2.Status().DefaultSoundfont; got != f2 {
		t.Fatalf("应返回排序首项 %q，got %q", f2, got)
	}
}
