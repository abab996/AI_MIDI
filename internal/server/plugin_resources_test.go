package server

import (
	"bytes"
	"encoding/json"
	"melodymate/internal/config"
	"melodymate/internal/soundlib"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginResourceSession(t *testing.T) {
	withTempSoundRoot(t)
	dir := filepath.Join(config.LibraryDir, "plugins", "com.test.ui@2")
	os.MkdirAll(filepath.Join(dir, "custom"), 0755)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"com.test.ui","name":"UI","version":"2","kind":"effect","ui":{"entry":"custom/editor.html"}}`), 0644)
	os.WriteFile(filepath.Join(dir, "custom", "editor.html"), []byte(`<script src="controller.js"></script>`), 0644)
	os.WriteFile(filepath.Join(dir, "custom", "controller.js"), []byte(`window.loaded=true`), 0644)
	src, _, err := soundlib.SyncInstalledMM(dir)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(nil, nil)
	request := func(method, path, body string, auth bool, origin ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "127.0.0.1"
		if len(origin) > 0 {
			req.Header.Set("Origin", origin[0])
		}
		if auth {
			req.Header.Set("X-MM-Token", router.AuthToken())
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	raw, _ := json.Marshal(map[string]any{"plugin": src.ID, "project": "P", "generation": 1})
	w := request("POST", "/api/engine/plugins/ui-ticket", string(raw), true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var ticket map[string]string
	json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&ticket)
	exchange := request("GET", "/api/engine/plugins/ui?ticket="+ticket["ticket"], "", false)
	if exchange.Code != 303 {
		t.Fatal(exchange.Code, exchange.Body.String())
	}
	url := exchange.Header().Get("Location")
	if !strings.HasSuffix(url, "custom/editor.html") {
		t.Fatal("manifest entry ignored", url)
	}
	if request("GET", "/api/engine/plugins/ui?ticket="+ticket["ticket"], "", false).Code != 401 {
		t.Fatal("ticket reused")
	}
	page := request("GET", url, "", false)
	if page.Code != 200 || !strings.Contains(page.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatal("sandbox document failed", page.Code)
	}
	asset := strings.TrimSuffix(url, "editor.html") + "controller.js"
	if request("GET", asset, "", false).Code != 200 {
		t.Fatal("relative asset denied")
	}
	module := request("GET", asset, "", false, "null")
	if module.Code != 200 || module.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("opaque iframe module denied", module.Code)
	}
	if request("GET", "/api/settings", "", true, "null").Code != 403 {
		t.Fatal("opaque origin can read regular API")
	}
	if request("GET", strings.Replace(asset, ticket["ticket"], "invalid", 1), "", false, "null").Code != 401 {
		t.Fatal("opaque origin bypassed capability")
	}
	if request("GET", strings.Replace(asset, ticket["ticket"], "invalid", 1), "", false).Code != 401 {
		t.Fatal("ungranted resource accessible")
	}
	revokeProjectResources("other", 2)
	if request("GET", asset, "", false).Code != 401 {
		t.Fatal("project switch did not revoke")
	}

	w = request("POST", "/api/engine/plugins/ui-ticket", string(raw), true)
	json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&ticket)
	exchange = request("GET", "/api/engine/plugins/ui?response=json&ticket="+ticket["ticket"], "", true)
	if exchange.Code != 200 {
		t.Fatal("WebView resource exchange must avoid navigation redirects", exchange.Code)
	}
	var direct map[string]string
	json.NewDecoder(bytes.NewReader(exchange.Body.Bytes())).Decode(&direct)
	if direct["session"] != ticket["ticket"] || !strings.HasSuffix(direct["url"], "custom/editor.html") || request("GET", direct["url"], "", false).Code != 200 {
		t.Fatal("invalid direct resource grant", direct)
	}
	if request("GET", "/api/engine/plugins/ui?response=json&ticket="+ticket["ticket"], "", true).Code != 401 {
		t.Fatal("JSON exchange reused ticket")
	}

	os.MkdirAll(filepath.Join(dir, "ui"), 0755)
	os.WriteFile(filepath.Join(dir, "ui", "index.html"), []byte(`<script>parent.postMessage({type:"mm.ready"},"*")</script>`), 0644)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"com.test.ui","name":"UI","version":"2","kind":"effect"}`), 0644)
	w = request("POST", "/api/engine/plugins/ui-ticket", string(raw), true)
	json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&ticket)
	exchange = request("GET", "/api/engine/plugins/ui?response=json&ticket="+ticket["ticket"], "", true)
	json.NewDecoder(bytes.NewReader(exchange.Body.Bytes())).Decode(&direct)
	page = request("GET", direct["url"], "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "mm.ready") {
		t.Fatal("default index document must be served without directory redirect", page.Code, page.Header().Get("Location"))
	}
}
