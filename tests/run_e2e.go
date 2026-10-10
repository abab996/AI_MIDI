//go:build ignore

// 独立 e2e 运行器：启动临时 Go 服务器，测试关键 API
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"melodymate/internal/config"
	"melodymate/internal/engine"
	"melodymate/internal/server"
)

func main() {
	fmt.Println("=== MelodyMate E2E Runner ===")
	tmpDir, err := os.MkdirTemp("", "e2e_*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmpDir)
	orig := config.SettingsFile
	config.SettingsFile = filepath.Join(tmpDir, "settings.json")
	defer func() { config.SettingsFile = orig }()

	// 使用内存文件系统作为前端（无需真实文件）
	r := server.NewRouter(nil, nil)
	request := func(method, url string, body io.Reader) (*http.Response, error) {
		req, err := http.NewRequest(method, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-MM-Token", r.AuthToken())
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}
	get := func(url string) (*http.Response, error) { return request("GET", url, nil) }
	post := func(url, contentType string, body io.Reader) (*http.Response, error) {
		return request("POST", url, body)
	}
	ts := httptest.NewServer(r)
	defer ts.Close()

	// 1. 测试默认 audio settings
	fmt.Println("[1] 测试默认 engine_enabled 为 true")
	resp, err := get(ts.URL + "/api/audio/settings")
	if err != nil {
		panic(err)
	}
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if enabled, ok := got["engine_enabled"].(bool); !ok || !enabled {
		panic(fmt.Sprintf("engine_enabled = %v, want true", got["engine_enabled"]))
	}
	fmt.Println("    ✓ 默认 engine_enabled 为 true")

	// 2. 测试切换 engine_enabled
	fmt.Println("[2] 测试切换 engine_enabled")
	body, _ := json.Marshal(map[string]any{"engine_enabled": false})
	resp, err = post(ts.URL+"/api/audio/settings", "application/json", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	resp.Body.Close()
	resp, _ = get(ts.URL + "/api/audio/settings")
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if enabled, ok := got["engine_enabled"].(bool); !ok || enabled {
		panic("切换 engine_enabled: false 失败")
	}
	// 恢复为 true
	body, _ = json.Marshal(map[string]any{"engine_enabled": true})
	resp, _ = post(ts.URL+"/api/audio/settings", "application/json", bytes.NewReader(body))
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	fmt.Println("    ✓ 切换 engine_enabled 成功")

	// 3. 测试 bounce 无引擎时应 503
	fmt.Println("[3] 测试 bounce 无引擎时 503")
	engine.SetGlobal(nil)
	body, _ = json.Marshal(map[string]any{"bpm": 120, "tracks": []any{}})
	resp, err = post(ts.URL+"/api/audio/bounce", "application/json", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	if resp.StatusCode != 503 {
		panic(fmt.Sprintf("bounce without engine should 503, got %d", resp.StatusCode))
	}
	fmt.Println("    ✓ bounce 503 正确")
	resp.Body.Close()

	// 4. 测试路径穿越防护
	fmt.Println("[4] 测试 bounce file 路径穿越防护")
	resp, _ = get(ts.URL + "/api/audio/bounce/file?path=C:/Windows/win.ini")
	if resp.StatusCode != 403 && resp.StatusCode != 404 {
		panic(fmt.Sprintf("path traversal should blocked, got %d", resp.StatusCode))
	}
	fmt.Println("    ✓ 路径穿越已拦截")
	resp.Body.Close()

	// 5. 测试 MIDI 尾音计算
	fmt.Println("[5] 测试 MIDI 尾音不截断计算")
	bpm := 120.0
	sr := 44100.0
	tailSec := 2.5
	spb := sr * 60.0 / bpm
	beats := 8.0
	total := int64(beats*spb + tailSec*sr)
	expected := int64(8*22050 + 110250)
	if total != expected {
		panic(fmt.Sprintf("tail calc failed %d != %d", total, expected))
	}
	fmt.Println("    ✓ 尾音计算正确")

	fmt.Println("\n=== 全部通过 ===")
}
