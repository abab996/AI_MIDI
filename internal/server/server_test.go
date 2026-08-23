package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"aimidi/internal/config"
	"aimidi/internal/project"
)

func setupTestServer(t *testing.T) (*httptest.Server, func()) {
	tmpDir, err := os.MkdirTemp("", "aimidi_server_test_*")
	if err != nil {
		t.Fatal(err)
	}

	config.ProjectsDir = filepath.Join(tmpDir, "projects")
	config.SettingsFile = filepath.Join(tmpDir, "settings.json")
	_ = os.MkdirAll(config.ProjectsDir, 0755)

	router := NewRouter(nil, nil)
	ts := httptest.NewServer(router)

	cleanup := func() {
		ts.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return ts, cleanup
}

func TestHealthEndpoint(t *testing.T) {
	ts, cleanup := setupTestServer(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var res map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if res["ok"] != true {
		t.Errorf("got ok = %v, want true", res["ok"])
	}
}

func TestSettingsEndpoints(t *testing.T) {
	ts, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. PUT settings
	payload := map[string]any{
		"api_key":          "test-key",
		"base_url":         "https://api.openai.com",
		"model":            "gpt-4o",
		"thinking_enabled": true,
	}
	b, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/settings failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("PUT status = %d, want 200", resp.StatusCode)
	}

	// 2. GET settings
	respGet, err := http.Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings failed: %v", err)
	}
	defer respGet.Body.Close()

	var gotSettings map[string]any
	_ = json.NewDecoder(respGet.Body).Decode(&gotSettings)
	// api_key 脱敏返回（"test-key" ≤8 位 → 全掩码），不再明文下发
	if gotSettings["api_key"] != "••••••••" || gotSettings["model"] != "gpt-4o" {
		t.Errorf("unexpected settings: %+v", gotSettings)
	}

	// 3. PUT 掩码值 = 保持已保存密钥不被清空/覆盖
	payloadKeep, _ := json.Marshal(map[string]any{
		"api_key":          "••••••••",
		"base_url":         "https://api.openai.com",
		"model":            "gpt-4o",
		"thinking_enabled": true,
	})
	reqKeep, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", bytes.NewReader(payloadKeep))
	reqKeep.Header.Set("Content-Type", "application/json")
	respKeep, err := client.Do(reqKeep)
	if err != nil {
		t.Fatalf("PUT /api/settings (masked) failed: %v", err)
	}
	respKeep.Body.Close()
	if respKeep.StatusCode != http.StatusOK {
		t.Fatalf("PUT (masked) status = %d, want 200", respKeep.StatusCode)
	}
	if key := config.LoadSettings().APIKey; key != "test-key" {
		t.Errorf("masked PUT should keep saved key, got %q", key)
	}
}

func TestProjectsEndpoints(t *testing.T) {
	ts, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. POST create project（返回与打开项目一致的完整载荷：meta/任务/文件等）
	createBody, _ := json.Marshal(map[string]string{"name": "HTTP测试项目"})
	resp, err := http.Post(ts.URL+"/api/projects", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /api/projects failed: %v", err)
	}
	var created map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	createdMetaRaw, _ := json.Marshal(created["meta"])
	var meta project.ProjectMeta
	_ = json.Unmarshal(createdMetaRaw, &meta)

	if meta.ID == "" || meta.Name != "HTTP测试项目" {
		t.Fatalf("create project meta invalid: %+v", created)
	}
	if created["current_task_id"] == nil || created["settings"] == nil {
		t.Fatalf("create project payload missing fields: %+v", created)
	}

	// 2. GET projects list
	respList, err := http.Get(ts.URL + "/api/projects")
	if err != nil {
		t.Fatalf("GET /api/projects failed: %v", err)
	}
	var list []project.ProjectEntry
	_ = json.NewDecoder(respList.Body).Decode(&list)
	respList.Body.Close()

	if len(list) != 1 || list[0].ID != meta.ID {
		t.Fatalf("projects list mismatch: %+v", list)
	}

	// 3. GET project detail / open（结构：meta{id,name} + current_task_id + midi_files + …）
	respDetail, err := http.Get(ts.URL + "/api/projects/" + meta.ID)
	if err != nil {
		t.Fatalf("GET /api/projects/%s failed: %v", meta.ID, err)
	}
	var detail map[string]any
	_ = json.NewDecoder(respDetail.Body).Decode(&detail)
	respDetail.Body.Close()

	detailMeta, _ := detail["meta"].(map[string]any)
	if detailMeta["name"] != "HTTP测试项目" || detailMeta["id"] != meta.ID {
		t.Fatalf("project detail meta invalid: %+v", detail)
	}
	if detail["current_task_id"] == nil || detail["midi_files"] == nil ||
		detail["display_messages"] == nil || detail["tasks"] == nil {
		t.Fatalf("project detail missing fields: %+v", detail)
	}
}

func TestAnswerEndpoint(t *testing.T) {
	ts, cleanup := setupTestServer(t)
	defer cleanup()

	// 1. 创建项目
	createBody, _ := json.Marshal(map[string]string{"name": "提问回答测试项目"})
	resp, err := http.Post(ts.URL+"/api/projects", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /api/projects failed: %v", err)
	}
	var created map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	createdMetaRaw, _ := json.Marshal(created["meta"])
	var meta project.ProjectMeta
	_ = json.Unmarshal(createdMetaRaw, &meta)

	// 2. 测试 POST /api/answer (无效提问ID返回错误并正常结束)
	answerPayload := map[string]any{
		"project_id":  meta.ID,
		"question_id": "non-existent-qid",
		"answers":     []any{},
	}
	payloadBytes, _ := json.Marshal(answerPayload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/answer", bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	ansResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/answer failed: %v", err)
	}
	defer ansResp.Body.Close()

	if ansResp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", ansResp.StatusCode)
	}
}

func TestModelsEndpointPOST(t *testing.T) {
	ts, cleanup := setupTestServer(t)
	defer cleanup()

	// 测试 POST /api/models 接口安全接收 JSON 请求体
	modelsPayload := map[string]string{
		"api_key":  "dummy-key",
		"base_url": "https://api.openai.com",
	}
	payloadBytes, _ := json.Marshal(modelsPayload)

	resp, err := http.Post(ts.URL+"/api/models", "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		t.Fatalf("POST /api/models failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var res map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if res["models"] == nil {
		t.Fatalf("expected models array in response, got %+v", res)
	}
}
