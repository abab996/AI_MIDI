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

	"melodymate/internal/config"
	"melodymate/internal/project"
	"melodymate/internal/soundlib"
)

// withTempProjectRoot 把数据根/项目目录/注册表一起换绑到临时目录
func withTempProjectRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldLib, oldSettings, oldProjects := config.ProjectRoot, config.LibraryDir, config.SettingsFile, config.ProjectsDir
	config.ProjectRoot = root
	config.LibraryDir = filepath.Join(root, "Library")
	config.SettingsFile = filepath.Join(root, "settings.json")
	config.ProjectsDir = filepath.Join(root, "projects")
	project.ResetProjectsIndexCache()
	t.Cleanup(func() {
		config.ProjectRoot, config.LibraryDir = oldRoot, oldLib
		config.SettingsFile, config.ProjectsDir = oldSettings, oldProjects
		project.ResetProjectsIndexCache()
	})

	meta, err := project.CreateProject("接口测试项目")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return root, meta.ID
}

// arrangeRouter 只挂 /api/projects/ 的最小路由器
func arrangeRouter() *Router {
	r := &Router{mux: http.NewServeMux()}
	r.mux.HandleFunc("/api/projects/", r.handleProjectsSub)
	return r
}

func doArrange(t *testing.T, r *Router, method, target string, body []byte) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func TestArrangementOpsEndpoint(t *testing.T) {
	root, pid := withTempProjectRoot(t)
	r := arrangeRouter()

	// 注册一个 SF2 音源（走真实导入通道的落盘逻辑：直接建文件 + 登记）
	dir := filepath.Join(root, "Library", "soundfonts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sf2Path := filepath.Join(dir, "GeneralUser GS.sf2")
	if err := os.WriteFile(sf2Path, testSF2("GeneralUser GS", 4), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _, err := soundlib.EnsureSF2Source(sf2Path, "GeneralUser GS")
	if err != nil {
		t.Fatalf("EnsureSF2Source: %v", err)
	}

	// 无编排文件时：ops 也能用（服务端建默认 v2 结构）
	body, _ := json.Marshal(map[string]any{
		"client": "ui",
		"ops": []map[string]any{
			{"op": "add_rack_item", "sourceId": src.ID, "presetId": "0:48"},
		},
	})
	code, out := doArrange(t, r, http.MethodPost, "/api/projects/"+pid+"/arrangement/ops", body)
	if code != 200 {
		t.Fatalf("ops 失败: %d %v", code, out)
	}
	if rev, _ := out["rev"].(float64); rev != 1 {
		t.Fatalf("rev = %v, want 1", out["rev"])
	}
	arr, _ := out["arrangement"].(map[string]any)
	if arr == nil {
		t.Fatalf("响应缺少 arrangement: %v", out)
	}
	changed, _ := out["changed"].([]any)
	if len(changed) != 1 || changed[0] != "rack" {
		t.Fatalf("changed = %v, want [rack]", out["changed"])
	}
	rack, _ := arr["rack"].(map[string]any)
	items, _ := rack["items"].([]any)
	if len(items) != 2 { // 默认内置合成器 + 新增 SF2
		t.Fatalf("机架条目数 = %d, want 2: %v", len(items), items)
	}
	var itemID string
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["sourceId"] == src.ID {
			itemID, _ = m["id"].(string)
		}
	}
	if itemID == "" {
		t.Fatalf("未找到新入架条目: %v", items)
	}

	// 指派给轨道 0 + 改混音
	body, _ = json.Marshal(map[string]any{
		"ops": []map[string]any{
			{"op": "setTrackSource", "track": 0, "itemId": itemID, "presetId": "0:48"},
			{"op": "setTrackMix", "track": 0, "gain": 0.6, "pan": 0.2},
		},
	})
	code, out = doArrange(t, r, http.MethodPost, "/api/projects/"+pid+"/arrangement/ops", body)
	if code != 200 {
		t.Fatalf("ops 失败: %d %v", code, out)
	}
	arr, _ = out["arrangement"].(map[string]any)
	tracks, _ := arr["tracks"].([]any)
	tm, _ := tracks[0].(map[string]any)
	source, _ := tm["source"].(map[string]any)
	if source["itemId"] != itemID || source["presetId"] != "0:48" {
		t.Fatalf("轨道音源未更新: %v", source)
	}
	if gain, _ := tm["volume"].(float64); gain != 0.6 {
		t.Fatalf("volume = %v, want 0.6", tm["volume"])
	}

	// GET 读回的是落盘后的 v2 数据
	code, _ = doArrange(t, r, http.MethodGet, "/api/projects/"+pid+"/arrangement", nil)
	if code != 200 {
		t.Fatalf("GET arrangement 失败: %d", code)
	}

	// 整份 PUT 与 ops 共用 rev 序列
	raw, _ := json.Marshal(arr)
	code, out = doArrange(t, r, http.MethodPut, "/api/projects/"+pid+"/arrangement", raw)
	if code != 200 {
		t.Fatalf("PUT arrangement 失败: %d %v", code, out)
	}
	// rev 由服务端单调推进：两次 ops（1、2）之后整份 PUT 应为 3
	if rev, _ := out["rev"].(float64); rev != 3 {
		t.Fatalf("PUT 后 rev = %v, want 3", out["rev"])
	}

	// 错误路径
	code, out = doArrange(t, r, http.MethodPost, "/api/projects/"+pid+"/arrangement/ops",
		[]byte(`{"ops":[{"op":"setTrackSource","track":0,"itemId":"rk_nope"}]}`))
	if code != http.StatusBadRequest {
		t.Fatalf("非法 itemId 应 400，实际 %d %v", code, out)
	}
	if detail, _ := out["detail"].(string); !strings.Contains(detail, "不在机架") {
		t.Fatalf("错误提示不可解释: %q", detail)
	}

	code, _ = doArrange(t, r, http.MethodPost, "/api/projects/"+pid+"/arrangement/ops", []byte(`{"ops":[]}`))
	if code != http.StatusBadRequest {
		t.Fatalf("空 ops 应 400，实际 %d", code)
	}

	code, _ = doArrange(t, r, http.MethodPost, "/api/projects/"+pid+"/arrangement/ops", []byte(`{ 坏 json`))
	if code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 %d", code)
	}

	code, _ = doArrange(t, r, http.MethodGet, "/api/projects/"+pid+"/arrangement/ops", nil)
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("GET ops 应 405，实际 %d", code)
	}

	// 未知项目：明确报"项目不存在"，而不是写盘失败的原始错误
	code, out = doArrange(t, r, http.MethodPost, "/api/projects/nope/arrangement/ops",
		[]byte(`{"ops":[{"op":"addRackItem","sourceId":"builtin:synth"}]}`))
	if code != http.StatusBadRequest {
		t.Fatalf("未知项目 ops 应 400，实际 %d %v", code, out)
	}
	if detail, _ := out["detail"].(string); !strings.Contains(detail, "项目不存在") {
		t.Fatalf("未知项目错误提示不可解释: %q", detail)
	}
}

func TestArrangementOpsRejectsUnknownTool(t *testing.T) {
	_, pid := withTempProjectRoot(t)
	r := arrangeRouter()
	code, out := doArrange(t, r, http.MethodPost, "/api/projects/"+pid+"/arrangement/ops",
		[]byte(`{"ops":[{"op":"dropDatabase"}]}`))
	if code != http.StatusBadRequest {
		t.Fatalf("未知操作应 400，实际 %d %v", code, out)
	}
	if detail, _ := out["detail"].(string); !strings.Contains(detail, "未知操作") {
		t.Fatalf("错误提示不可解释: %q", detail)
	}
}
