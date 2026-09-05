package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aimidi/internal/update"
)

// installTestManifest 把 Router 的清单客户端指向测试服务器并预填缓存
func installTestManifest(t *testing.T, r *Router, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	r.updater.client = update.NewClient(srv.URL)
	if _, err := r.updater.client.Fetch(context.Background()); err != nil {
		t.Fatalf("预填清单缓存失败: %v", err)
	}
}

// TestUpdateGateWhitelisted 强制更新网关白名单
func TestUpdateGateWhitelisted(t *testing.T) {
	yes := []string{"/api/update/check", "/api/update/apply", "/api/update/progress", "/api/health", "/api/version", "/api/client-error"}
	for _, p := range yes {
		if !updateGateWhitelisted(p) {
			t.Errorf("%s 应在白名单内", p)
		}
	}
	no := []string{"/api/chat", "/api/answer", "/api/tasks", "/api/projects", "/api/settings"}
	for _, p := range no {
		if updateGateWhitelisted(p) {
			t.Errorf("%s 不应在白名单内", p)
		}
	}
}

// TestMandatoryGateBlocksMutations 必要更新未完成时：POST 被 426 拦截，
// GET 与白名单端点放行（fail-open 设计：清单拉不到时不拦）。
func TestMandatoryGateBlocksMutations(t *testing.T) {
	r := NewRouter(nil, nil)
	installTestManifest(t, r, `{"version":"99.0.0","mandatory":true,"notes":"x"}`)

	ts := httptest.NewServer(r)
	defer ts.Close()

	// 改写型请求被拦截
	resp, err := http.Post(ts.URL+"/api/chat", "application/json", strings.NewReader(`{"project_id":"p","message":"hi"}`))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("POST /api/chat 应被 426 拦截, got %d", resp.StatusCode)
	}

	// GET 不拦
	resp2, err := http.Get(ts.URL + "/api/version")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/version 应放行, got %d", resp2.StatusCode)
	}

	// 白名单内的非 GET 放行（client-error 是 POST）
	resp3, err := http.Post(ts.URL+"/api/client-error", "application/json", strings.NewReader(`{"message":"test"}`))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode == http.StatusUpgradeRequired {
		t.Fatalf("白名单端点不应被网关拦截")
	}
}

// TestUpdateCheckResponse check 响应字段与下载链接按 GOOS 选取
func TestUpdateCheckResponse(t *testing.T) {
	r := NewRouter(nil, nil)
	installTestManifest(t, r, `{"version":"99.0.0","date":"2026-09-06","notes":"日志内容",`+
		`"downloads":{"windows":"https://dl.example/e.exe","linux":"https://dl.example/t.gz"}}`)

	ts := httptest.NewServer(r)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/update/check")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Available   bool   `json:"available"`
		Mandatory   bool   `json:"mandatory"`
		Current     string `json:"current"`
		Latest      string `json:"latest"`
		Date        string `json:"date"`
		Notes       string `json:"notes"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !out.Available || out.Latest != "99.0.0" || out.Date != "2026-09-06" || out.Notes != "日志内容" {
		t.Fatalf("响应字段不正确: %+v", out)
	}
	if out.DownloadURL == "" || !strings.HasPrefix(out.DownloadURL, "https://dl.example/") {
		t.Fatalf("download_url 应按当前平台选取: %q", out.DownloadURL)
	}
}

// TestUpdateCheckFailOpen 清单不可达时 check 返回 available=false（静默）
func TestUpdateCheckFailOpen(t *testing.T) {
	r := NewRouter(nil, nil)
	r.updater.client = update.NewClient("http://127.0.0.1:1/update.json") // 不可达

	ts := httptest.NewServer(r)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/update/check")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Available   bool `json:"available"`
		CheckFailed bool `json:"check_failed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if out.Available || !out.CheckFailed {
		t.Fatalf("清单不可达应 fail-open: %+v", out)
	}
}
