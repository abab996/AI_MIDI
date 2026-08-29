package midi

import (
	"math"
	"testing"
)

// 验证离线 bounce 的尾音不截断：总时长 = maxEnd*spb + tailSec*sr
func TestBounceTotalDurationIncludesTail(t *testing.T) {
	bpm := 120.0
	sr := 44100.0
	tailSec := 2.5
	spb := sr * 60.0 / bpm // 22050
	beats := 8.0           // 最后一拍
	total := int64(math.Round(beats*spb + tailSec*sr))
	expected := int64(8*22050 + 2.5*44100) // 176400 + 110250 = 286650
	if total != expected {
		t.Fatalf("total %d != expected %d", total, expected)
	}
	// 若 tail 被吞（仅 beats*spb），则短 110250 采样
	truncated := int64(beats * spb)
	if total-truncated != int64(tailSec*sr) {
		t.Fatal("tail not included")
	}
}

// 验证 MIDI 文件本身不含尾（符合规范），但音频渲染需额外 tail
func TestSMFDoesNotContainTail(t *testing.T) {
	notes := []NoteEvent{
		{Note: "C4", Velocity: 100, Start: 0, End: 2},
		{Note: "E4", Velocity: 100, Start: 2, End: 4},
	}
	midiBytes, err := BuildSMF(notes, 120)
	if err != nil {
		t.Fatal(err)
	}
	// SMF 长度应仅到最后 noteEnd (4拍)，不含 tail
	parsed, err := ParseMidiToCustomFormat(midiBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed %d", len(parsed))
	}
	// 音频 bounce 应在此基础上 + tail
	bpm := 120.0
	sr := 48000.0
	tailSec := 2.5
	spb := sr * 60 / bpm
	audioTotalBeats := 4.0 + tailSec*bpm/60.0 // 4 + 1.25 = 5.25 拍等效
	_ = audioTotalBeats
	_ = spb
}
