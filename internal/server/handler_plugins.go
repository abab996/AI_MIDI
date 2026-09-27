package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"melodymate/internal/config"
	"melodymate/internal/engine"
	"melodymate/internal/soundlib"
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

	case "/ui":
		/* P6：MM 插件 UI 的静态承载（前端 mm-ui.html 用 sandbox iframe 加载它）。
		   路径必须落在 Library/plugins/<id>@<ver>/ 内：逐条校验，拒绝任何穿越。
		   query：plugin=<插件 id>、asset=<包内相对路径，默认取 manifest 的 ui.entry> */
		q := req.URL.Query()
		pluginID := strings.TrimSpace(q.Get("plugin"))
		if pluginID == "" || strings.ContainsAny(pluginID, "/\\") || strings.Contains(pluginID, "..") {
			writeError(w, http.StatusBadRequest, "plugin 参数非法")
			return
		}
		asset := strings.TrimSpace(q.Get("asset"))
		if asset == "" {
			asset = "ui/index.html"
		}
		clean := filepath.ToSlash(filepath.Clean(asset))
		if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
			writeError(w, http.StatusBadRequest, "asset 路径非法")
			return
		}
		pluginsRoot := filepath.Join(config.LibraryDir, "plugins")
		entries, _ := os.ReadDir(pluginsRoot)
		var base string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if name == pluginID || strings.HasPrefix(name, pluginID+"@") {
				base = filepath.Join(pluginsRoot, name)
				break
			}
		}
		if base == "" {
			writeError(w, http.StatusNotFound, "插件未安装："+pluginID)
			return
		}
		full := filepath.Join(base, filepath.FromSlash(clean))
		rel, rerr := filepath.Rel(base, full)
		if rerr != nil || strings.HasPrefix(rel, "..") {
			writeError(w, http.StatusBadRequest, "asset 越界")
			return
		}
		if st, serr := os.Stat(full); serr != nil || st.IsDir() {
			writeError(w, http.StatusNotFound, "插件 UI 资源不存在："+clean)
			return
		}
		/* 只服务插件 UI 需要的静态类型（不当作通用文件服务器） */
		switch strings.ToLower(filepath.Ext(full)) {
		case ".html", ".js", ".css", ".json", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".woff", ".woff2", ".ttf":
		default:
			writeError(w, http.StatusForbidden, "不允许的资源类型")
			return
		}
		http.ServeFile(w, req, full)

	case "/sync":
		/* 把 Library/plugins/ 下已安装的 MM 插件同步进音源注册表：
		   命令行 mm install 或手工拷贝之后，插件要能在「声音管理」与素材库
		   「音源」页签里出现；同时作为设置页「刷新」按钮的入口。 */
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		added, updated, failed := soundlib.SyncAllInstalledMM()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "updated": updated, "failed": failed})

	case "/installed":
		/* 已安装的 MM 插件清单（设置页「声音管理·MM 插件」分组用） */
		list := soundlib.ListInstalledMM()
		writeJSON(w, http.StatusOK, map[string]any{"count": len(list), "plugins": list})

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
			Format string `json:"format"`
			Path   string `json:"path"`
			Track  int    `json:"track"`
			Slot   *int   `json:"slot"` // 指针：nil = 未给（乐器），0 = 第一个 FX 槽
			Master bool   `json:"master"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		/* P6：format=mm 走 MM 运行时（path 是 .mm 包解包后的原生库路径） */
		if in.Format == "mm" {
			track := in.Track
			if in.Master {
				track = -1
			}
			slot := -1
			if in.Slot != nil {
				slot = *in.Slot
			}
			state, err := sup.LoadMmPlugin(track, slot, in.Path)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mm": state})
			return
		}
		track := in.Track
		if in.Master {
			track = -1
		}
		slot := -1
		if in.Slot != nil {
			slot = *in.Slot
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
			Slot   *int `json:"slot"`
			Master bool `json:"master"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		track := in.Track
		if in.Master {
			track = -1
		}
		slot := -1
		if in.Slot != nil {
			slot = *in.Slot
		}
		if err := sup.UnloadPlugin(track, slot); err != nil {
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
			Slot   *int    `json:"slot"`
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
		if in.Slot != nil {
			slot = *in.Slot
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
			Slot   *int `json:"slot"`
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
		if in.Slot != nil {
			slot = *in.Slot
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
