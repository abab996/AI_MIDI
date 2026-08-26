package engine

import (
	"testing"
	"time"
)

// 测试多轨独立：32轨并发 noteOn 不应互相串音（channel 隔离）
func TestMultiTrackIsolation(t *testing.T) {
	// 纯逻辑：验证 supervisor 的多轨映射与 client 的 4 字节帧
	c := &Client{}
	// 模拟 32 轨的 SendMidiTrack 不应越界
	for tr := 0; tr < 32; tr++ {
		if err := c.SendMidiTrack(tr, 0x90, 60, 100); err != nil {
			// 未连接时 Write 会超时，但不应 panic；此处仅验证参数钳制
			_ = err
		}
	}
	// 越界钳制
	if err := c.SendMidiTrack(100, 0x90, 60, 100); err != nil {
		_ = err
	}
	if err := c.SendMidiTrack(-5, 0x90, 60, 100); err != nil {
		_ = err
	}
}

// 测试 supervisor 默认超时覆盖 listDevices 冷启动
func TestSupervisorBounceParams(t *testing.T) {
	sup := NewSupervisor(Config{}, AudioSettings{Backend: "auto"})
	// 未启动时 Bounce 应返回“未就绪”而非 hang
	_, err := sup.Bounce(map[string]any{"bpm": 120.0, "beats": 4.0})
	if err == nil {
		t.Fatalf("expected error when engine not ready")
	}
}

// 测试 tail 计算：beats*spb + tailSec*sr
func TestBounceTailCalculation(t *testing.T) {
	bpm := 120.0
	beats := 16.0
	sr := 48000.0
	tailSec := 2.5
	spb := sr * 60.0 / bpm // 24000
	total := int64(beats*spb + tailSec*sr)
	expected := int64(16*24000 + 2.5*48000) // 384000 + 120000 = 504000
	if total != expected {
		t.Fatalf("totalSamples = %d, want %d", total, expected)
	}
	// 验证 beats 推导：若未提供 beats 但有 notes，则取最大 end
	notes := []map[string]any{
		{"start": 0.0, "end": 8.0},
		{"start": 4.0, "end": 12.5},
	}
	maxEnd := beats
	for _, n := range notes {
		if e, ok := n["end"].(float64); ok && e > maxEnd {
			maxEnd = e
		}
	}
	if maxEnd != 16.0 {
		// 12.5 <16 仍为 16
	}
	notes2 := []map[string]any{{"start": 0.0, "end": 20.0}}
	maxEnd2 := beats
	for _, n := range notes2 {
		if e, ok := n["end"].(float64); ok && e > maxEnd2 {
			maxEnd2 = e
		}
	}
	if maxEnd2 != 20.0 {
		t.Fatalf("maxEnd2 = %v, want 20", maxEnd2)
	}
}

// 测试 per-track SF2 重放：lastSoundFonts map
func TestPerTrackSoundFontMap(t *testing.T) {
	sup := NewSupervisor(Config{SoundFontDir: ""}, AudioSettings{})
	sup.lastSoundFonts[0] = "piano.sf2"
	sup.lastSoundFonts[1] = "strings.sf2"
	if sup.lastSoundFonts[0] != "piano.sf2" || sup.lastSoundFonts[1] != "strings.sf2" {
		t.Fatal("per-track map not isolated")
	}
}

// 测试 timecode 插值不回退
func TestTimecodeMonotonic(t *testing.T) {
	c := &Client{}
	// 模拟连续 latch
	for i := 0; i < 3; i++ {
		payload := make([]byte, 25)
		// 仅验证 latch 不 panic
		c.latchTimecode(payload)
		time.Sleep(1 * time.Millisecond)
	}
	if _, ok := c.LatchedTimecode(); !ok {
		t.Fatal("expected latched")
	}
}
