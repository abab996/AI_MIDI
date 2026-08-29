package app

// 桥接事件流集成测试：ChatStreamStart 经 mock LLM（分块延迟输出）驱动
// chat.ChatStream，断言桥事件逐条即时推送（多次 chat_delta + 真实时间
// 跨度），验证桌面模式"逐字流式"的 Go 侧链路。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aimidi/internal/config"
	"aimidi/internal/project"
)

// isolateConfig 把 config 的目录常量指到临时目录，并写入指向 mockLLM 的 settings
func isolateConfig(t *testing.T, llmURL string) {
	t.Helper()
	dir := t.TempDir()
	old := [6]string{config.ProjectRoot, config.SettingsFile, config.ProjectsDir, config.OutputDir, config.LibraryDir, config.DoingDir}
	t.Cleanup(func() {
		config.ProjectRoot, config.SettingsFile, config.ProjectsDir = old[0], old[1], old[2]
		config.OutputDir, config.LibraryDir, config.DoingDir = old[3], old[4], old[5]
	})
	config.ProjectRoot = dir
	config.SettingsFile = filepath.Join(dir, "settings.json")
	config.ProjectsDir = filepath.Join(dir, "projects")
	config.OutputDir = filepath.Join(dir, "output")
	config.LibraryDir = filepath.Join(dir, "Library")
	config.DoingDir = filepath.Join(dir, "doing")
	if err := os.MkdirAll(config.ProjectsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"api_key": "test-key", "base_url": llmURL, "model": "m"}
	b, _ := json.Marshal(settings)
	if err := os.WriteFile(config.SettingsFile, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChatStreamStartStreamsIncrementally(t *testing.T) {
	// 1. mock LLM：reasoning 2 块 + content 10 块，每块 60ms（总时长 ~0.8s）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		send := func(delta map[string]any) {
			chunk := map[string]any{
				"id": "x", "object": "chat.completion.chunk", "model": "m",
				"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
			time.Sleep(60 * time.Millisecond)
		}
		for i := 0; i < 2; i++ {
			send(map[string]any{"role": "assistant", "reasoning_content": fmt.Sprintf("思考%d。", i)})
		}
		for i := 0; i < 10; i++ {
			send(map[string]any{"content": fmt.Sprintf("片段%d。", i)})
		}
		send(map[string]any{})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	isolateConfig(t, srv.URL)

	// 2. 创建真实项目（走 project 包，保证 index/目录一致）
	meta, err := project.CreateProject("桥接测试")
	if err != nil {
		t.Fatalf("创建项目失败: %v", err)
	}

	// 3. App + 注入 emitter（记录事件与时间）
	a := NewApp()
	a.Startup(context.Background())
	var mu sync.Mutex
	type ev struct {
		t  time.Time
		ev map[string]any
	}
	var events []ev
	a.emitter = func(streamID string, event map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev{time.Now(), event})
	}

	// 4. 启动桥接流
	if err := a.ChatStreamStart(meta.ID, "请做一个长文测试", false, "", "sid1"); err != nil {
		t.Fatalf("ChatStreamStart: %v", err)
	}

	// 5. 等待流结束（__end 标记）
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		var last map[string]any
		if n > 0 {
			last = events[n-1].ev
		}
		mu.Unlock()
		if n > 0 && last["__end"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("流超时未结束，事件数=%d", n)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 6. 断言
	mu.Lock()
	defer mu.Unlock()
	var deltas []time.Time
	var types []string
	for _, e := range events {
		types = append(types, fmt.Sprint(e.ev["type"]))
		if e.ev["type"] == "chat_delta" {
			deltas = append(deltas, e.t)
		}
	}
	if len(deltas) < 5 {
		t.Fatalf("chat_delta 事件过少: %d（types=%v）", len(deltas), types)
	}
	span := deltas[len(deltas)-1].Sub(deltas[0])
	if span < 300*time.Millisecond {
		t.Fatalf("chat_delta 时间跨度过小: %v —— 事件未逐条即时推送", span)
	}
	if types[0] != "chat" {
		t.Fatalf("首个事件应为 chat: %v", types[:3])
	}
	if types[len(types)-2] != "done" || events[len(events)-1].ev["__end"] != true {
		t.Fatalf("收尾事件异常: %v", types[len(types)-3:])
	}
	t.Logf("chat_delta=%d，跨度=%v，序列=%v", len(deltas), span, types)
}
