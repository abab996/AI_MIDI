package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"melodymate/internal/engine"
)

// handleEnginePluginsSub /api/engine/plugins*：VST3 宿主的 HTTP 入口。
//
// 为什么不用 Wails 绑定：浏览器模式（开发/验收脚本）没有 window.go，插件清单与
// 装载必须能被脚本与前端同一条路径驱动；Wails 绑定作为桌面模式的快路径并存。
//
// 引擎未启动时统一返回 503 + 可解释文案（浏览器模式没有原生引擎）。
func (r *Router) handleEnginePluginsSub(w http.ResponseWriter, req *http.Request) {
	sub := strings.TrimPrefix(req.URL.Path, "/api/engine/plugins")
	sup := engine.Get()
	if sup == nil {
		writeError(w, http.StatusServiceUnavailable, "原生引擎未启动：VST3 插件仅在桌面版可用（浏览器模式请用 SF2/采样包音源）")
		return
	}

	switch sub {
	case "", "/":
		list, err := sup.ListPlugins()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"count": list.Count, "plugins": list.Plugins, "formats": list.Formats,
		})

	case "/scan":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in struct {
			Dirs []string `json:"dirs"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		list, err := sup.ScanPlugins(in.Dirs, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"count": list.Count, "plugins": list.Plugins, "formats": list.Formats,
		})

	case "/state":
		state, err := sup.PluginState()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, state)

	case "/load":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in struct {
			ID     string `json:"id"`
			Track  int    `json:"track"`
			Slot   int    `json:"slot"`
			Master bool   `json:"master"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		track := in.Track
		if in.Master {
			track = -1
		}
		slot := -1
		if _, has := pluginSlotOf(req); has {
			slot = in.Slot
		}
		if err := sup.LoadPlugin(track, slot, in.ID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		state, _ := sup.PluginState()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})

	case "/unload":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in struct {
			Track  int  `json:"track"`
			Slot   int  `json:"slot"`
			Master bool `json:"master"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		track := in.Track
		if in.Master {
			track = -1
		}
		if err := sup.UnloadPlugin(track, in.Slot); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "/params":
		track, slot, ok := pluginTargetOf(req)
		if !ok {
			return
		}
		out, err := sup.PluginParams(track, slot)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)

	case "/param":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in struct {
			Track  int     `json:"track"`
			Slot   int     `json:"slot"`
			Master bool    `json:"master"`
			Index  int     `json:"index"`
			Value  float64 `json:"value"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		track := in.Track
		if in.Master {
			track = -1
		}
		slot := -1
		if _, has := pluginSlotOf(req); has {
			slot = in.Slot
		}
		if err := sup.SetPluginParam(track, slot, in.Index, in.Value); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := sup.PluginParams(track, slot)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		writeJSON(w, http.StatusOK, out)

	case "/editor":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in struct {
			Track  int  `json:"track"`
			Slot   int  `json:"slot"`
			Master bool `json:"master"`
			Close  bool `json:"close"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		if in.Close {
			if err := sup.ClosePluginEditor(); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "opened": false})
			return
		}
		track := in.Track
		if in.Master {
			track = -1
		}
		slot := -1
		if _, has := pluginSlotOf(req); has {
			slot = in.Slot
		}
		if err := sup.OpenPluginEditor(track, slot); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "opened": true})

	default:
		writeError(w, http.StatusNotFound, "unknown plugin endpoint")
	}
}

// pluginSlotOf 判断请求里是否**显式**给了 slot（区分"乐器"与"槽 0"）
func pluginSlotOf(req *http.Request) (int, bool) {
	raw := req.URL.Query().Get("slot")
	if raw == "" {
		return -1, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return -1, false
	}
	return n, true
}

// pluginTargetOf 从 query 解析 {track|master, slot}
func pluginTargetOf(req *http.Request) (int, int, bool) {
	q := req.URL.Query()
	track := 0
	if v := q.Get("track"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, 0, false
		}
		track = n
	}
	if q.Get("master") == "1" || q.Get("master") == "true" {
		track = -1
	}
	slot := -1
	if n, has := pluginSlotOf(req); has {
		slot = n
	}
	return track, slot, true
}
