package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

		"aimidi/internal/chat"
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


/* ---- panic 恢复中间件（P0-2）---- */

func TestRecoverPanicBeforeWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &capturingWriter{ResponseWriter: rec}
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)

	func() {
		defer recoverPanic(cw, req)
		panic("人为测试 panic")
	}()

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var res map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if res["detail"] != "服务内部错误，请查看日志后重试" {
		t.Errorf("detail = %v", res["detail"])
	}
}

func TestRecoverPanicAfterSSEStart(t *testing.T) {
	/* SSE 已开始输出：无法补写 500，recover 不得再 panic、状态保持 200 */
	rec := httptest.NewRecorder()
	cw := &capturingWriter{ResponseWriter: rec}
	cw.WriteHeader(http.StatusOK)
	_, _ = cw.Write([]byte("data: {\"type\":\"progress\"}\n\n"))
	req := httptest.NewRequest(http.MethodPost, "/api/chat", nil)

	func() {
		defer recoverPanic(cw, req)
		panic("流中途 panic")
	}()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（流已开始）", rec.Code)
	}
}

func TestRecoverPanicNoPanic(t *testing.T) {
	/* 无 panic 时 recoverPanic 直接返回，不影响正常响应 */
	rec := httptest.NewRecorder()
	cw := &capturingWriter{ResponseWriter: rec}
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	writeJSON(cw, http.StatusOK, map[string]any{"ok": true})
	recoverPanic(cw, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"ok":true`)) {
		t.Fatalf("正常响应被破坏: %d %s", rec.Code, rec.Body.String())
	}
}

/* ---- 前端异常上报端点（P0-7）---- */

func TestClientErrorEndpoint(t *testing.T) {
	ts, cleanup := setupTestServer(t)
	defer cleanup()

	body := `{"message":"Test error","source":"app.js","line":10,"column":2,"page":"/chat.html"}`
	resp, err := http.Post(ts.URL+"/api/client-error", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/client-error failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	/* 空消息应被拒绝 */
	resp2, err := http.Post(ts.URL+"/api/client-error", "application/json", strings.NewReader(`{"message":"  "}`))
	if err != nil {
		t.Fatalf("POST empty failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("empty message status = %d, want 400", resp2.StatusCode)
	}
}


/* ---- 版本端点（P0-6 About 卡片）---- */

func TestVersionEndpoint(t *testing.T) {
	ts, cleanup := setupTestServer(t)
	defer cleanup()

	resp, err := http.Get(ts.URL + "/api/version")
	if err != nil {
		t.Fatalf("GET /api/version failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var res map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	/* 单测环境未注入 wails.json 版本，应回落 dev 而非空串 */
	if res["version"] != "dev" {
		t.Errorf("version = %v, want dev", res["version"])
	}
		if res["name"] != config.WindowTitle {
			t.Errorf("name = %v", res["name"])
		}
	}

	func TestUndoEditAndRestoreEndpoints(t *testing.T) {
		ts, cleanup := setupTestServer(t)
		defer cleanup()

		// 1. 创建项目
		createPayload := map[string]any{"name": "UndoRestoreProject"}
		cb, _ := json.Marshal(createPayload)
		resp, err := http.Post(ts.URL+"/api/projects", "application/json", bytes.NewReader(cb))
		if err != nil {
			t.Fatalf("POST /api/projects failed: %v", err)
		}
		defer resp.Body.Close()
		var createRes map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&createRes)
		metaMap, _ := createRes["meta"].(map[string]any)
		projectID, _ := metaMap["id"].(string)
		if projectID == "" {
			t.Fatalf("创建项目失败，未返回 ID: %+v", createRes)
		}

		// 2. 模拟写入一个 MIDI 文件
		midiDir := project.MidiDir(projectID)
		_ = os.MkdirAll(midiDir, 0755)
		midiFile := filepath.Join(midiDir, "ai_generated.mid")
		_ = os.WriteFile(midiFile, []byte("midi data"), 0644)
		project.SaveMidiManifest(projectID, []project.MidiFileInfo{
			{Name: "ai_generated.mid", Path: midiFile, Size: 9},
		})

		// 3. 设置 session 消息历史并调用撤回修改接口（messages/undo-edit 模式：删除/移入回收站）
		sess := chat.GetSession(projectID)
		st := sess.GetTaskState("")
		sess.MidiFiles = []project.MidiFileInfo{
			{Name: "ai_generated.mid", Path: midiFile, Size: 9},
		}
		st.ChatDisplay = []map[string]any{
			{"role": "user", "content": "生成 midi", "timestamp": float64(100)},
			{"role": "assistant", "content": "已生成", "timestamp": float64(101)},
		}
		st.FullHistory = []map[string]any{
			{"role": "user", "content": "生成 midi", "timestamp": float64(100)},
			{
				"role":    "assistant",
				"content": "已生成",
				"tool_calls": []any{
					map[string]any{
						"function": map[string]any{
							"name": "create_midi",
							"arguments": `{"filename":"ai_generated.mid"}`,
						},
					},
				},
			},
		}

		undoPayload := map[string]any{
			"index": 0,
		}
		ub, _ := json.Marshal(undoPayload)
		undoReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/"+projectID+"/messages/undo-edit", bytes.NewReader(ub))
		undoReq.Header.Set("Content-Type", "application/json")
		undoResp, err := http.DefaultClient.Do(undoReq)
		if err != nil {
			t.Fatalf("POST messages/undo-edit failed: %v", err)
		}
		defer undoResp.Body.Close()
		if undoResp.StatusCode != http.StatusOK {
			t.Fatalf("POST messages/undo-edit status = %d, want 200", undoResp.StatusCode)
		}
		var undoRes map[string]any
		_ = json.NewDecoder(undoResp.Body).Decode(&undoRes)
		undoFiles, _ := undoRes["files"].([]any)
		if len(undoFiles) != 0 {
			t.Fatalf("撤回修改后文件列表应为空，实际: %d", len(undoFiles))
		}
		if _, err := os.Stat(midiFile); !os.IsNotExist(err) {
			t.Fatalf("文件应当已被移入回收站")
		}

		// 4. 调用放弃撤回接口（messages/recall：恢复文件与历史）
		restoreReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/projects/"+projectID+"/messages/recall", nil)
		restoreResp, err := http.DefaultClient.Do(restoreReq)
		if err != nil {
			t.Fatalf("POST messages/recall failed: %v", err)
		}
		defer restoreResp.Body.Close()
		if restoreResp.StatusCode != http.StatusOK {
			t.Fatalf("POST messages/recall status = %d, want 200", restoreResp.StatusCode)
		}
		var restoreRes map[string]any
		_ = json.NewDecoder(restoreResp.Body).Decode(&restoreRes)
		files, _ := restoreRes["files"].([]any)
		if len(files) != 1 {
			t.Fatalf("恢复后期望 1 个文件，实际: %d", len(files))
		}
		if _, err := os.Stat(midiFile); err != nil {
			t.Fatalf("原文件应当已被恢复: %v", err)
		}
	}
