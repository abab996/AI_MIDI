package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"melodymate/internal/engine"
)

// pluginRouter 只挂 /api/engine/plugins/ 的最小路由器。supervisor 用
// 「未启动」实例：engine.Get() 非 nil 让中间件与子路由可达，而下面的用例
// 全部在触达 IPC（Ready 等 5s 超时）之前就被输入校验拦截，快速且确定。
func pluginRouter(t *testing.T) *Router {
	t.Helper()
	sup := engine.NewSupervisor(engine.Config{}, engine.AudioSettings{EngineEnabled: false})
	engine.SetGlobal(sup)
	t.Cleanup(func() { engine.SetGlobal(nil) })
	r := &Router{mux: http.NewServeMux()}
	r.mux.HandleFunc("/api/engine/plugins/", r.handleEnginePluginsSub)
	return r
}

// TestPluginDecodeErrorsReturn400 解码失败必须 400，而不是静默按零值处理。
func TestPluginDecodeErrorsReturn400(t *testing.T) {
	r := pluginRouter(t)
	for _, tc := range []struct{ name, method, target, body string }{
		{"scan", "POST", "/api/engine/plugins/scan", "{bad json"},
		{"unload", "POST", "/api/engine/plugins/unload", "{bad json"},
		{"editor", "POST", "/api/engine/plugins/editor", "{bad json"},
		{"param", "POST", "/api/engine/plugins/param", "{bad json"},
	} {
		code, out := doSound(t, r, tc.method, tc.target, []byte(tc.body))
		if code != http.StatusBadRequest {
			t.Fatalf("%s: 解码错误应 400，got %d (%v)", tc.name, code, out)
		}
	}
}

// TestPluginGenerationValidation generation 显式提供但解析失败/类型不对 → 400
// （缺省不提供仍放行走默认 0）。
func TestPluginGenerationValidation(t *testing.T) {
	r := pluginRouter(t)

	code, out := doSound(t, r, "GET", "/api/engine/plugins/params?project=p&generation=abc", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("GET generation=abc 应 400，got %d (%v)", code, out)
	}

	code, out = doSound(t, r, "POST", "/api/engine/plugins/editor", []byte(`{"generation":"abc"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("POST generation 字符串应 400，got %d (%v)", code, out)
	}

	code, out = doSound(t, r, "POST", "/api/engine/plugins/editor", []byte(`{"generation":[1]}`))
	if code != http.StatusBadRequest {
		t.Fatalf("POST generation 数组应 400，got %d (%v)", code, out)
	}

	// 不带 generation 的空 body：不触发校验，直接落在 /unload 解码 400——不得 5xx
	code, _ = doSound(t, r, "POST", "/api/engine/plugins/unload", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("空 body /unload 应解码 400，got %d", code)
	}
}

// TestPluginParamBatchLimit 批量下发超过单次上限必须 400（在触达引擎前拦截）。
func TestPluginParamBatchLimit(t *testing.T) {
	r := pluginRouter(t)
	changes := make([]map[string]any, 0, 300)
	for i := 0; i < 300; i++ {
		changes = append(changes, map[string]any{"index": i, "value": 0.5})
	}
	body, _ := json.Marshal(map[string]any{"track": 0, "changes": changes})
	code, out := doSound(t, r, "POST", "/api/engine/plugins/param", body)
	if code != http.StatusBadRequest {
		t.Fatalf("changes 超限应 400，got %d (%v)", code, out)
	}
}
