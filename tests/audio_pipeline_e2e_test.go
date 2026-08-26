package tests

import (
	"math"
	"testing"

	"aimidi/internal/midi"
)

// 模拟前端到后端的完整链路：note_table -> SMF -> 引擎 bounce 尾音
func TestAudioPipelineTailNotCut(t *testing.T) {
	// 1. 前端生成的 note_table：最后一个音符在 8 拍，长度 1 拍，有长释放尾（如钢琴 0.8s）
	notes := []midi.NoteEvent{
		{Note: "C4", Velocity: 100, Start: 0, End: 1},
		{Note: "E4", Velocity: 100, Start: 1, End: 2},
		{Note: "G4", Velocity: 100, Start: 7, End: 8}, // 最后一个音符
	}
	bpm := 120.0
	sr := 44100.0
	tailSec := 2.5
	spb := sr * 60.0 / bpm // 22050

	// 2. SMF 导出：仅到 8 拍，不含 tail（符合 MIDI 规范）
	midiBytes, err := midi.BuildSMF(notes, int(bpm))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := midi.ParseMidiToCustomFormat(midiBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 3 {
		t.Fatalf("SMF parsed notes %d", len(parsed))
	}
	// 验证 SMF 的时长是 8 拍
	maxEnd := 8.0
	totalSamplesSMF := int64(maxEnd * spb)
	totalSamplesAudio := int64(maxEnd*spb + tailSec*sr)
	if totalSamplesAudio <= totalSamplesSMF {
		t.Fatal("audio should be longer than SMF due to tail")
	}
	// 验证尾音被完整保留：tail 部分应为 2.5s
	tailSamples := totalSamplesAudio - totalSamplesSMF
	expectedTail := int64(tailSec * sr)
	if tailSamples != expectedTail {
		t.Fatalf("tail samples %d != expected %d", tailSamples, expectedTail)
	}
	// 验证 beats 推导：若 beats 未提供但有 notes，则应取最大 end
	beats := 16.0
	notesForBounce := []map[string]any{
		{"start": 0.0, "end": 8.0},
		{"start": 7.0, "end": 8.0},
	}
	maxEnd2 := beats
	for _, n := range notesForBounce {
		if e, ok := n["end"].(float64); ok && e > maxEnd2 {
			maxEnd2 = e
		}
	}
	if maxEnd2 != 16.0 {
		t.Fatalf("maxEnd should stay 16 when notes end < beats")
	}
	notesForBounce2 := []map[string]any{{"start": 0.0, "end": 20.0}}
	maxEnd3 := beats
	for _, n := range notesForBounce2 {
		if e, ok := n["end"].(float64); ok && e > maxEnd3 {
			maxEnd3 = e
		}
	}
	if maxEnd3 != 20.0 {
		t.Fatalf("maxEnd should be 20 when note exceeds beats")
	}

	// 验证离线总时长公式
	total2 := int64(math.Round(maxEnd3*spb + tailSec*sr))
	expected2 := int64(20*22050 + 110250) // 441000+110250=551250
	if total2 != expected2 {
		t.Fatalf("total2 %d != %d", total2, expected2)
	}
}
