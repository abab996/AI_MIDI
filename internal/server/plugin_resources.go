package server

import (
	"encoding/json"
	"fmt"
	"melodymate/internal/config"
	"melodymate/internal/engine"
	"melodymate/internal/soundlib"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type pluginResourceGrant struct {
	Root, Entry, Project string
	Generation           int64
	Expires              time.Time
}

var resourceMu sync.Mutex
var resourceTickets = map[string]pluginResourceGrant{}
var resourceSessions = map[string]pluginResourceGrant{}

func pluginUIRoot(id, version string) (string, string, error) {
	root := ""
	if src, err := soundlib.Get(id); err == nil && src.Kind == soundlib.KindMM {
		if !src.Enabled {
			return "", "", fmt.Errorf("插件已禁用")
		}
		root = soundlib.ResolvePath(src.Path)
	} else {
		for _, p := range soundlib.ListInstalledMM() {
			if p["id"] == id && (version == "" || p["version"] == version) {
				if root != "" {
					return "", "", fmt.Errorf("多个版本已安装，请使用注册表 sourceId")
				}
				root, _ = p["dir"].(string)
			}
		}
	}
	if root == "" {
		return "", "", fmt.Errorf("插件版本未安装")
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return "", "", err
	}
	var mf struct {
		UI *struct {
			Entry string `json:"entry"`
		} `json:"ui"`
	}
	if err = json.Unmarshal(raw, &mf); err != nil {
		return "", "", err
	}
	entry := "ui/index.html"
	if mf.UI != nil && mf.UI.Entry != "" {
		entry = mf.UI.Entry
	}
	entry = filepath.ToSlash(filepath.Clean(entry))
	if filepath.IsAbs(entry) || entry == ".." || strings.HasPrefix(entry, "../") {
		return "", "", fmt.Errorf("无效 UI 入口")
	}
	full := filepath.Join(root, filepath.FromSlash(entry))
	if err = engine.ValidateMmPluginPath(filepath.Join(config.LibraryDir, "plugins"), full); err != nil {
		return "", "", err
	}
	return root, entry, nil
}
func (r *Router) handleBoundUITicket(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, 405, "仅支持 POST")
		return
	}
	var in struct {
		Plugin, Version, Project string
		Generation               int64
	}
	if json.NewDecoder(req.Body).Decode(&in) != nil {
		writeError(w, 400, "无效插件 UI 请求")
		return
	}
	root, entry, err := pluginUIRoot(in.Plugin, in.Version)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ticket := issueUITicket()
	resourceMu.Lock()
	for key, g := range resourceTickets {
		if time.Now().After(g.Expires) {
			delete(resourceTickets, key)
		}
	}
	for key, g := range resourceSessions {
		if time.Now().After(g.Expires) {
			delete(resourceSessions, key)
		}
	}
	resourceTickets[ticket] = pluginResourceGrant{root, entry, in.Project, in.Generation, time.Now().Add(uiTicketTTL)}
	resourceMu.Unlock()
	writeJSON(w, 200, map[string]any{"ticket": ticket})
}
func (r *Router) exchangePluginUITicket(w http.ResponseWriter, req *http.Request) {
	ticket := req.URL.Query().Get("ticket")
	resourceMu.Lock()
	grant, ok := resourceTickets[ticket]
	delete(resourceTickets, ticket)
	resourceMu.Unlock()
	if !ok || !consumeUITicket(ticket) || time.Now().After(grant.Expires) {
		writeError(w, 401, "插件 UI 凭据已失效")
		return
	}
	session := ticket
	grant.Expires = time.Now().Add(12 * time.Hour)
	resourceMu.Lock()
	resourceSessions[session] = grant
	resourceMu.Unlock()
	if req.URL.Query().Get("response") == "json" {
		writeJSON(w, http.StatusOK, map[string]string{"session": session, "url": "/api/engine/plugins/resources/" + session + "/" + grant.Entry})
		return
	}
	http.Redirect(w, req, "/api/engine/plugins/resources/"+session+"/"+grant.Entry, http.StatusSeeOther)
}
func (r *Router) handlePluginResource(w http.ResponseWriter, req *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/api/engine/plugins/resources/"), "/", 2)
	if len(parts) != 2 {
		writeError(w, 401, "无效资源会话")
		return
	}
	resourceMu.Lock()
	grant, ok := resourceSessions[parts[0]]
	if ok && time.Now().After(grant.Expires) {
		delete(resourceSessions, parts[0])
		ok = false
	}
	resourceMu.Unlock()
	if !ok {
		writeError(w, 401, "资源会话已关闭")
		return
	}
	clean := filepath.Clean(filepath.FromSlash(parts[1]))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		writeError(w, 400, "资源路径越界")
		return
	}
	full := filepath.Join(grant.Root, clean)
	if err := engine.ValidateMmPluginPath(grant.Root, full); err != nil {
		writeError(w, 403, "资源路径无效")
		return
	}
	switch strings.ToLower(filepath.Ext(full)) {
	case ".html", ".js", ".mjs", ".css", ".json", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".woff", ".woff2", ".ttf":
	default:
		writeError(w, 403, "不允许的资源类型")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'self'")
	file, err := os.Open(full)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, req)
		return
	}
	// ServeFile redirects index.html to a directory; resource capabilities expose
	// files, so directory navigation cannot be used (and Wails cannot redirect it).
	http.ServeContent(w, req, info.Name(), info.ModTime(), file)
}
func revokeProjectResources(project string, generation int64) {
	resourceMu.Lock()
	defer resourceMu.Unlock()
	for key, g := range resourceSessions {
		if g.Project != project || g.Generation != generation {
			delete(resourceSessions, key)
		}
	}
	for key, g := range resourceTickets {
		if g.Project != project || g.Generation != generation {
			delete(resourceTickets, key)
		}
	}
}
func (r *Router) revokePluginResource(w http.ResponseWriter, req *http.Request) {
	var in struct{ Session string }
	if req.Method != http.MethodPost || json.NewDecoder(req.Body).Decode(&in) != nil {
		writeError(w, 400, "无效会话")
		return
	}
	resourceMu.Lock()
	delete(resourceSessions, in.Session)
	delete(resourceTickets, in.Session)
	resourceMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}
