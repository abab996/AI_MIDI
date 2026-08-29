package chat

import (
	"encoding/json"
	"testing"
)

// 回归测试：工具参数畸形时 parseToolArgs 必须返回非 nil 空 map。
// 此前 _ = json.Unmarshal 后直接 rawArgs["bpm"] = x，模型输出
// "null"/畸形 JSON 时触发 nil map 写入 panic（桌面事件桥 goroutine
// 无 recover，整个应用闪退）
func TestParseToolArgsNeverNil(t *testing.T) {
	cases := []string{
		"",                // 空串
		"   ",             // 纯空白
		"null",            // JSON null → Unmarshal 后为 nil map
		"{bad json",       // 畸形 JSON
		`"just a string"`, // 合法 JSON 但非对象
		"[1,2,3]",         // 合法 JSON 数组 → 无法 unmarshal 进 map，报错
	}
	for _, in := range cases {
		got := parseToolArgs(in)
		if got == nil {
			t.Fatalf("parseToolArgs(%q) = nil, want non-nil map", in)
		}
		// 关键：必须真的可写（这是原 panic 的触发点）
		got["bpm"] = 120
	}
}

func TestParseToolArgsValidInput(t *testing.T) {
	got := parseToolArgs(`{"filename":"song.mid","bpm":90}`)
	if got == nil {
		t.Fatal("parseToolArgs = nil for valid object")
	}
	if got["filename"] != "song.mid" {
		t.Fatalf("filename = %v, want song.mid", got["filename"])
	}
	if bpm, ok := got["bpm"].(float64); !ok || bpm != 90 {
		t.Fatalf("bpm = %v (%T), want 90", got["bpm"], got["bpm"])
	}
	// 不会被后续兜底逻辑误改：写入别的键不影响原值
	got["bpm"] = 120.0
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("result not marshalable: %v", err)
	}
}
