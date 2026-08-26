package app

import (
	"os"
	"strings"
	"testing"
)

func TestSplashFollowsEngineReady(t *testing.T) {
	data, err := os.ReadFile("../../main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(data)
	// 不应再有 1400ms 固定等待
	if strings.Contains(s, "1400 * time.Millisecond") {
		t.Fatal("main.go still has 1400ms fake splash wait, should be 5000ms with engine-driven close")
	}
	if strings.Contains(s, "5000 * time.Millisecond") == false {
		t.Fatal("main.go should have 5000ms splash max wait")
	}
	// 不应再有 OnDomReady 的 400ms 假等待
	if strings.Contains(s, "400 * time.Millisecond") && strings.Contains(s, "OnDomReady") {
		// 检查 OnDomReady 是否还包含 sleep
		// 允许其他地方的 400ms，但 OnDomReady 块内不应有
		idx := strings.Index(s, "OnDomReady")
		if idx != -1 {
			segment := s[idx : idx+500]
			if strings.Contains(segment, "Sleep(400") {
				t.Fatal("OnDomReady still has 400ms fake wait")
			}
		}
	}
	// 应该有引擎就绪轮询
	if !strings.Contains(s, "engineSup.Status()") || !strings.Contains(s, "StateReady") {
		t.Fatal("main.go should poll engineSup.Status() for splash close")
	}
	if !strings.Contains(s, "HideWindow") {
		// 检查 supervisor 是否隐藏窗口
		supData, err := os.ReadFile("../engine/supervisor.go")
		if err == nil && !strings.Contains(string(supData), "HideWindow") {
			t.Fatal("supervisor.go should hide engine console window")
		}
	}
}
