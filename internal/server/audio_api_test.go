package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aimidi/internal/config"
	"aimidi/internal/engine"
)

func setupTestRouter(t *testing.T) (*Router, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "audio_api_test_*")
	if err != nil {
		t.Fatal(err)
	}
	orig := config.SettingsFile
	config.SettingsFile = filepath.Join(tmpDir, "settings.json")
	// 清理
	cleanup := func() {
		config.SettingsFile = orig
		os.RemoveAll(tmpDir)
		// 重置全局 engine
		engine.SetGlobal(nil)
	}
	return NewRouter(nil, nil), cleanup
}

func TestAudioSettingsBackend(t *testing.T) {
	r, cleanup := setupTestRouter(t)
	defer cleanup()

	// GET 默认应为 auto
	req := httptest.NewRequest("GET", "/api/audio/settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET settings code %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["backend"] != "auto" {
		t.Fatalf("backend = %v, want auto", got["backend"])
	}

	// POST webaudio
	body, _ := json.Marshal(map[string]any{"backend": "webaudio"})
	req = httptest.NewRequest("POST", "/api/audio/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("POST webaudio code %d body %s", w.Code, w.Body.String())
	}
	// 再次 GET 应持久化
	req = httptest.NewRequest("GET", "/api/audio/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["backend"] != "webaudio" {
		t.Fatalf("after POST backend = %v, want webaudio", got["backend"])
	}

	// 非法值应 400
	body, _ = json.Marshal(map[string]any{"backend": "invalid"})
	req = httptest.NewRequest("POST", "/api/audio/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("invalid backend should 400, got %d", w.Code)
	}
}

func TestAudioBounceRequiresEngine(t *testing.T) {
	r, cleanup := setupTestRouter(t)
	defer cleanup()
	// 未启动引擎时应 503
	body, _ := json.Marshal(map[string]any{"bpm": 120, "tracks": []any{}})
	req := httptest.NewRequest("POST", "/api/audio/bounce", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("bounce without engine should 503, got %d", w.Code)
	}
}

func TestBounceFileServingPathTraversalBlocked(t *testing.T) {
	r, cleanup := setupTestRouter(t)
	defer cleanup()
	req := httptest.NewRequest("GET", "/api/audio/bounce/file?path=C:/Windows/win.ini", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 && w.Code != 404 {
		t.Fatalf("path traversal should blocked, got %d", w.Code)
	}
}

func TestSettingsButtonConsistency(t *testing.T) {
	// 静态检查：settings.html 的 ASIO 按钮不应有硬编码深底
	data, err := os.ReadFile(filepath.Join("..", "..", "frontend", "settings.html"))
	if err != nil {
		t.Skip("skip static check: " + err.Error())
	}
	if bytes.Contains(data, []byte(`style="background:#2a3b4c`)) {
		t.Fatal("settings.html ASIO button still has hardcoded dark background, should be unified with .fn")
	}
	// style.css 不应有 #openPanelBtn 深底覆盖
	css, err := os.ReadFile(filepath.Join("..", "..", "frontend", "style.css"))
	if err == nil {
		if bytes.Contains(css, []byte("#openPanelBtn")) && bytes.Contains(css, []byte("#2a3b4c")) {
			t.Fatal("style.css still has #openPanelBtn dark override")
		}
	}
}
