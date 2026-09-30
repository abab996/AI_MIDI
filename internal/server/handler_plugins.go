package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"melodymate/internal/config"
	"melodymate/internal/engine"
	"melodymate/internal/mm"
	"melodymate/internal/plugins"
	"melodymate/internal/project"
	"melodymate/internal/soundlib"
)

// ── 插件界面一次性凭据（ui_ticket）─────────────────────────────
//
// 为什么需要：插件 UI 由 <iframe src="/api/engine/plugins/ui?..."> 加载，
// 浏览器导航带不了 X-MM-Token 自定义头。若为了放行 iframe 而整体豁免
// 该端点，本机任意进程/页面就能拉取插件资源；若要求自定义头，iframe
// 会拿到 401 渲染成错误页。
//
// 折中：由已在令牌保护下的 /api/engine/plugins/ui-ticket 签发短时效、
// 一次性票，作为查询参数拼在 iframe URL 上。票只能用于 /ui 端点，
// 用完即废，且随进程重启清空。

const uiTicketTTL = 60 * time.Second

var (
	uiTicketsMu sync.Mutex
	uiTickets   = map[string]time.Time{} // ticket -> 过期时刻
)

// issueUITicket 签发一次性插件界面凭据
func issueUITicket() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	ticket := hex.EncodeToString(buf)
	now := time.Now()
	uiTicketsMu.Lock()
	defer uiTicketsMu.Unlock()
	// 顺手清理过期票，避免无界增长
	for k, exp := range uiTickets {
		if now.After(exp) {
			delete(uiTickets, k)
		}
	}
	uiTickets[ticket] = now.Add(uiTicketTTL)
	return ticket
}

// consumeUITicket 校验并消费一次性凭据（成功即作废）
func consumeUITicket(ticket string) bool {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return false
	}
	now := time.Now()
	uiTicketsMu.Lock()
	defer uiTicketsMu.Unlock()
	exp, ok := uiTickets[ticket]
	if !ok {
		return false
	}
	delete(uiTickets, ticket) // 一次性：无论是否过期都移除
	return now.Before(exp)
}

// handleUITicket POST /api/engine/plugins/ui-ticket：为 iframe 签发一次性凭据。
// 该端点本身受启动令牌保护（非豁免），因此只有应用界面能拿到票。
func (r *Router) handleUITicket(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	ticket := issueUITicket()
	if ticket == "" {
		writeError(w, http.StatusInternalServerError, "签发插件界面凭据失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "expires_in": int(uiTicketTTL.Seconds())})
}

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
	case "/ui-ticket":
		r.handleUITicket(w, req)

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
		   query：plugin=<插件 id>、asset=<包内相对路径，默认取 manifest 的 ui.entry>

		   鉴权说明：本端点被 <iframe src> 直接加载，浏览器导航无法携带
		   X-MM-Token 自定义头，因此走一次性的 ui_ticket（由已在令牌保护下
		   的 /api/engine/plugins/ui-ticket 换取，仅限本端点、短时效）。 */
		q := req.URL.Query()
		if !consumeUITicket(q.Get("ticket")) {
			writeError(w, http.StatusUnauthorized, "插件界面访问凭据缺失或已失效：请从混音台重新打开插件界面")
			return
		}
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
		/* 安全响应头：该文档与应用同源。正常路径下它只被 mm-ui.html 的
		   sandbox iframe 加载；但对话里的链接可能把它当顶层文档打开——
		   sandbox 指令让浏览器对它强制启用沙箱（与 iframe 同等限制），
		   使其脚本拿不到主界面权限（cookie/同源 API 调用）。 */
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.ServeFile(w, req, full)

	case "/install":
		/* .mm 包安装两步式（能力授权对话框的后端）：
		   preview=true  → 只校验并返回声明能力/哈希/AI 标记，不落盘（弹确认框用）
		   preview=false → 带 grant 安装；声明未授权即拒绝（fail-closed）
		 pkg 必须落在 <数据根>/Library/plugin-src/ 或 Library/plugins/ 内 */
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in struct {
			Pkg     string   `json:"pkg"`
			Grant   []string `json:"grant"`
			Preview bool     `json:"preview"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		pkg := filepath.Clean(in.Pkg)
		if pkg == "" || !strings.HasSuffix(strings.ToLower(pkg), ".mm") {
			writeError(w, http.StatusBadRequest, "pkg 必须是 .mm 文件路径")
			return
		}
		allowed := false
		for _, root := range []string{filepath.Join(config.LibraryDir, "plugin-src"), filepath.Join(config.LibraryDir, "plugins")} {
			if rel, err := filepath.Rel(root, pkg); err == nil && !strings.HasPrefix(rel, "..") {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "只允许安装 Library/plugin-src 下的 .mm 包")
			return
		}
		dest := filepath.Join(config.LibraryDir, "plugins")
		if in.Preview {
			preview, err := mm.ValidateAndPreview(pkg, dest, in.Grant)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"preview": preview, "needsGrant": len(preview.MissingGrant) > 0})
			return
		}
		preview, err := mm.Install(pkg, dest, in.Grant)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		added, updated, failed := soundlib.SyncAllInstalledMM()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "installed": preview, "added": added, "updated": updated, "failed": failed,
		})

	case "/uninstall":
		/* 卸载已安装的 MM 插件（设置页 MM 分组用）。顺序很重要：
		   ① 先把引擎里占用它的槽位卸掉 —— Windows 上已加载的 DLL 被文件锁占用，
		      直接删会失败（这正是"卸载点了没反应"的常见成因）；
		   ② 再删安装目录（mm.Uninstall 只允许删 plugins/ 下的路径）；
		   ③ 最后清注册表条目，避免留下"幽灵条目"。 */
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		pluginID := strings.TrimSpace(in.ID)
		if pluginID == "" || strings.ContainsAny(pluginID, "/\\") || strings.Contains(pluginID, "..") {
			writeError(w, http.StatusBadRequest, "插件 id 非法")
			return
		}
		unloaded := []string{}
		if state, serr := sup.PluginState(); serr == nil {
			for _, key := range []string{"effects", "instruments"} {
				list, _ := state[key].([]any)
				for _, raw := range list {
					m, _ := raw.(map[string]any)
					if m == nil || fmt.Sprint(m["id"]) != pluginID {
						continue
					}
					track, slot := 0, -1
					if v, ok := m["track"].(float64); ok {
						track = int(v)
					}
					if v, ok := m["slot"].(float64); ok {
						slot = int(v)
					}
					if uerr := sup.UnloadPlugin(track, slot); uerr == nil {
						unloaded = append(unloaded, fmt.Sprintf("track=%d slot=%d", track, slot))
					}
				}
			}
		}
		dest := filepath.Join(config.LibraryDir, "plugins")
		entries, _ := os.ReadDir(dest)
		removed := []string{}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if e.Name() != pluginID && !strings.HasPrefix(e.Name(), pluginID+"@") {
				continue
			}
			dir := filepath.Join(dest, e.Name())
			/* 引擎里卸载是**延迟释放**的（槽位换下来后由释放队列在消息线程销毁，
			   约 500ms），所以文件锁不会立刻消失 —— 直接删会 Access is denied。
			   这里对"占用类"错误做短重试（最多约 4 秒），其余错误立刻上报。 */
			var derr error
			for attempt := 0; attempt < 14; attempt++ {
				derr = mm.Uninstall(dir)
				if derr == nil || !looksLikeFileBusy(derr) {
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
			if derr != nil {
				writeError(w, http.StatusConflict, "删除失败（插件可能仍被引擎占用，请先在混音台卸载并稍后重试）："+derr.Error())
				return
			}
			removed = append(removed, e.Name())
		}
		if len(removed) == 0 {
			writeError(w, http.StatusNotFound, "未找到已安装的插件："+pluginID)
			return
		}
		if rerr := soundlib.UninstallMMSource(pluginID); rerr != nil {
			writeError(w, http.StatusInternalServerError, "目录已删除，但注册表清理失败："+rerr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": pluginID, "removed": removed, "unloaded": unloaded})
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

	/* ── P5：插件二进制状态随工程保存 ──
	   状态不进 arrangement.json（可达数 MB），按槽位落在
	   <工程目录>/plugins/*.state；引擎重启重放后由守护器推回。 */
	case "/state/save":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in pluginStateRequest
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		target, dir, terr := in.target()
		if terr != nil {
			writeError(w, http.StatusBadRequest, terr.Error())
			return
		}
		blob, gerr := sup.PluginGetState(target.Track, target.Slot)
		if gerr != nil {
			writeError(w, http.StatusBadRequest, "导出插件状态失败："+gerr.Error())
			return
		}
		payload, derr := base64.StdEncoding.DecodeString(blob)
		if derr != nil {
			writeError(w, http.StatusInternalServerError, "引擎返回的状态不是合法 base64")
			return
		}
		store := plugins.NewStore(dir)
		if serr := store.Save(target, in.Ref, in.Format, payload); serr != nil {
			writeError(w, http.StatusBadRequest, serr.Error())
			return
		}
		/* 同时进内存缓存：引擎崩溃重启后守护器用它推回（详见 plugins 包注释） */
		plugins.Default().Put(target, in.Ref, in.Format, payload)
		plugins.Default().SetActiveProject(target.ProjectID)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "file": target.FileName(), "bytes": len(payload),
		})

	case "/state/load":
		if req.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		var in pluginStateRequest
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		target, dir, terr := in.target()
		if terr != nil {
			writeError(w, http.StatusBadRequest, terr.Error())
			return
		}
		store := plugins.NewStore(dir)
		env, lerr := store.Load(target)
		if lerr != nil {
			if os.IsNotExist(lerr) {
				writeError(w, http.StatusNotFound, "该槽位还没有保存过插件状态")
				return
			}
			writeError(w, http.StatusBadRequest, lerr.Error())
			return
		}
		/* 引用不一致时拒绝恢复：把 A 插件的状态灌进 B 插件轻则报错、重则崩溃 */
		if !env.Matches(in.Ref) {
			writeError(w, http.StatusConflict,
				"保存的状态属于另一个插件（"+env.Ref+"），拒绝恢复到当前插件")
			return
		}
		if serr := sup.PluginSetState(target.Track, target.Slot, env.Data); serr != nil {
			writeError(w, http.StatusBadRequest, "恢复插件状态失败："+serr.Error())
			return
		}
		payload, _ := env.Payload()
		plugins.Default().Put(target, env.Ref, env.Format, payload)
		plugins.Default().SetActiveProject(target.ProjectID)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "file": target.FileName(), "ref": env.Ref,
			"savedAt": env.SavedAt, "bytes": env.Bytes,
		})

	case "/state/list":
		projectID := strings.TrimSpace(req.URL.Query().Get("project"))
		if projectID == "" && req.Method == http.MethodPost {
			var in pluginStateRequest
			_ = json.NewDecoder(req.Body).Decode(&in)
			projectID = strings.TrimSpace(in.Project)
		}
		if projectID == "" {
			writeError(w, http.StatusBadRequest, "缺少 project（工程 ID）")
			return
		}
		dir, derr := project.ProjectDir(projectID)
		if derr != nil {
			writeError(w, http.StatusBadRequest, "工程不存在或不可读："+derr.Error())
			return
		}
		list, lerr := plugins.NewStore(dir).List()
		if lerr != nil {
			writeError(w, http.StatusInternalServerError, lerr.Error())
			return
		}
		files := make([]map[string]any, 0, len(list))
		for _, t := range list {
			files = append(files, map[string]any{
				"file": t.FileName(), "track": t.Track, "slot": t.Slot,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(files), "states": files})

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
		/* P6：format=mm 走 MM 运行时（path 是 .mm 包解包后的原生库路径）。
		   路径必须先落在已安装插件目录内：引擎对该路径直接 LoadLibrary，
		   不做目录约束，因此这里与 supervisor 各校验一次（双端防护）。 */
		if in.Format == "mm" {
			if err := engine.ValidateMmPluginPath(filepath.Join(config.LibraryDir, "plugins"), in.Path); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
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

// pluginStateRequest 插件状态读写请求体（/state/save、/state/load 共用）。
type pluginStateRequest struct {
	Project string `json:"project"` // 工程 ID（状态按工程存放）
	Track   int    `json:"track"`
	Slot    *int   `json:"slot"` // nil = 该轨乐器（与 /load 同一约定）
	Master  bool   `json:"master"`
	Ref     string `json:"ref"`    // 插件引用（校验"这份状态属于这个插件"）
	Format  string `json:"format"` // vst3 | mm（诊断用）
}

// target 解析成 plugins.Target 并返回该工程的数据目录。
func (in pluginStateRequest) target() (plugins.Target, string, error) {
	projectID := strings.TrimSpace(in.Project)
	if projectID == "" {
		return plugins.Target{}, "", fmt.Errorf("缺少 project（工程 ID）")
	}
	track := in.Track
	if in.Master {
		track = -1
	}
	slot := -1
	if in.Slot != nil {
		slot = *in.Slot
	}
	target := plugins.Target{ProjectID: projectID, Track: track, Slot: slot}
	if err := target.Valid(); err != nil {
		return plugins.Target{}, "", err
	}
	dir, err := project.ProjectDir(projectID)
	if err != nil {
		return plugins.Target{}, "", fmt.Errorf("工程不存在或不可读：%w", err)
	}
	return target, dir, nil
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

// looksLikeFileBusy 判断"文件被占用"类错误（Windows 上表现为 Access is denied /
// being used by another process）。只有这类错误值得重试；路径不存在等应立即上报。
func looksLikeFileBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	/* 只认"文件/目录被占用"的措辞：泛化的 permission denied 属于 ACL 问题，重试无意义 */
	for _, marker := range []string{
		"access is denied",
		"being used by another process",
		"sharing violation",
		"另一个程序正在使用此文件",
		"拒绝访问",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
