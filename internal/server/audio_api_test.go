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

// 后端设置项已移除：GET 不再返回 backend；POST 带 backend 也不生效
// （未知字段被忽略，其余字段照常保存）
func TestAudioSettingsBackendRemoved(t *testing.T) {
	r, cleanup := setupTestRouter(t)
	defer cleanup()

	req := newLocalRequest("GET", "/api/audio/settings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET settings code %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, has := got["backend"]; has {
		t.Fatalf("backend 键应已从响应中移除, got %v", got["backend"])
	}

	// 旧客户端仍可能发 backend：不应报错，且不影响保存
	body, _ := json.Marshal(map[string]any{"backend": "webaudio", "buffer_size": 256})
	req = newLocalRequest("POST", "/api/audio/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("POST with legacy backend code %d body %s", w.Code, w.Body.String())
	}
	req = newLocalRequest("GET", "/api/audio/settings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &got)
	if got["buffer_size"] != float64(256) {
		t.Fatalf("buffer_size 应被保存, got %v", got["buffer_size"])
	}
	if _, has := got["backend"]; has {
		t.Fatalf("backend 键不应回来")
	}
}

func TestAudioBounceRequiresEngine(t *testing.T) {
	r, cleanup := setupTestRouter(t)
	defer cleanup()
	// 未启动引擎时应 503
	body, _ := json.Marshal(map[string]any{"bpm": 120, "tracks": []any{}})
	req := newLocalRequest("POST", "/api/audio/bounce", bytes.NewReader(body))
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
	req := newLocalRequest("GET", "/api/audio/bounce/file?path=C:/Windows/win.ini", nil)
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
