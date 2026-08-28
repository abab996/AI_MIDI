package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aimidi/internal/config"
	"aimidi/internal/project"
)

// TestProjectBPMEndpoint 全局 BPM 端到端：POST 写入 → GET 读回 →
// 项目载荷携带 bpm；非法值 400。
func TestProjectBPMEndpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bpm_api_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	origProjects := config.ProjectsDir
	config.ProjectsDir = filepath.Join(tmpDir, "projects")
	project.ResetProjectsIndexCache()
	defer func() {
		config.ProjectsDir = origProjects
		project.ResetProjectsIndexCache()
	}()

	r, cleanup := setupTestRouter(t)
	defer cleanup()

	meta, err := project.CreateProject("BPM端点测试")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/projects/" + meta.ID

	// GET 默认 120
	w := httptest.NewRecorder()
	r.ServeHTTP(w, newLocalRequest("GET", base+"/bpm", nil))
	if w.Code != 200 {
		t.Fatalf("GET bpm code %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["bpm"].(float64) != 120 {
		t.Fatalf("default bpm = %v, want 120", got["bpm"])
	}

	// POST 写入 96 → GET 读回
	body, _ := json.Marshal(map[string]any{"bpm": 96})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newLocalRequest("POST", base+"/bpm", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("POST bpm code %d body %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newLocalRequest("GET", base+"/bpm", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["bpm"].(float64) != 96 {
		t.Fatalf("bpm after POST = %v, want 96", got["bpm"])
	}

	// 项目载荷携带 bpm（前端 applyProjectPayload / 编排加载依赖）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newLocalRequest("GET", base, nil))
	if w.Code != 200 {
		t.Fatalf("GET project code %d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["bpm"].(float64) != 96 {
		t.Fatalf("payload bpm = %v, want 96", got["bpm"])
	}

	// 非法值 400
	body, _ = json.Marshal(map[string]any{"bpm": 500})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newLocalRequest("POST", base+"/bpm", bytes.NewReader(body)))
	if w.Code != 400 {
		t.Fatalf("invalid bpm should 400, got %d", w.Code)
	}
}
