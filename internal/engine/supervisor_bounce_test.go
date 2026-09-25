package engine

import (
	"testing"
	"time"
)

// TestEstimateBounceTimeout 固定 30s 超时会杀掉长工程导出（引擎允许渲染
// 最长 600s 音频）：估算必须随素材时长放大并封顶。
func TestEstimateBounceTimeout(t *testing.T) {
	// 短渲染：16 拍 @120bpm = 8s + tail 2.5s → 2*10.5+60 = 81s
	got := estimateBounceTimeout(map[string]any{"bpm": 120.0, "beats": 16.0, "tailSec": 2.5})
	if got != 81*time.Second {
		t.Fatalf("短渲染超时 = %v, 期望 81s", got)
	}

	// 下限 30s：空参数（引擎按缺省 beats=16 渲染也不止 30s，但显式 0 拍必须兜底）
	if got := estimateBounceTimeout(map[string]any{"beats": 0.0, "tailSec": 0.0}); got < 30*time.Second {
		t.Fatalf("超时下限应不低于 30s, 实际 %v", got)
	}

	// notes[].end 放宽：end=1440 拍 @120bpm = 720s → 2*720+60 = 1500s（25min 封顶正好覆盖）
	got = estimateBounceTimeout(map[string]any{
		"bpm":   120.0,
		"beats": 16.0,
		"notes": []any{map[string]any{"end": 1440.0}},
	})
	if got != 25*time.Minute {
		t.Fatalf("长渲染超时应封顶 25min（覆盖 600s 音频×2+60s 最坏情形）, 实际 %v", got)
	}

	// 引擎允许的最长渲染（600s 音频 = 1200 拍 @120bpm + tail 30s）：
	// 2*(600+30)+60 = 1320s = 22min，必须高于 20min 旧上限且不被 25min 截断
	got = estimateBounceTimeout(map[string]any{
		"bpm":     120.0,
		"beats":   1200.0,
		"tailSec": 30.0,
	})
	if got != 22*time.Minute {
		t.Fatalf("最坏情形超时 = %v, 期望 22min（600s 音频×2+60s+tail）", got)
	}

	// tracks[].clips[].start+length 放宽（与引擎 computeBeats 对齐）
	// end=120 拍 @60bpm = 120s，tailSec 缺省 0 → 2*120+60 = 300s
	got = estimateBounceTimeout(map[string]any{
		"bpm":   60.0,
		"beats": 4.0,
		"tracks": []any{map[string]any{"clips": []any{
			map[string]any{"start": 100.0, "length": 20.0},
		}}},
	})
	want := 2*120*time.Second + 60*time.Second
	if got != want {
		t.Fatalf("tracks 展开超时 = %v, 期望 %v", got, want)
	}
}
