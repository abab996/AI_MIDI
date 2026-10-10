package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"melodymate/internal/engine"
	"melodymate/internal/soundlib"
)

// handleSoundSub /api/sound/* 子路由：音源注册表（声音管理）与采样包内容。
//
//	GET    /api/sound/sources[?kind=&q=]   列举（含磁盘实况）
//	POST   /api/sound/sources              新增（采样包目录 / 插件登记）
//	PUT    /api/sound/sources/{id}         改名 / 启停 / 改目录 / 改标签
//	DELETE /api/sound/sources/{id}[?file=1] 删除（SF2 可连带删磁盘文件）
//	GET    /api/sound/presets?id=          SF2 预设列表（bank:program）
//	GET    /api/sound/samples?pack=&sub=   采样包内容列举
//	POST   /api/sound/import/sf2?name=     上传 SF2（落盘 + 入册）
//	POST   /api/sound/migrate              重新扫描磁盘并入册（设置页"重新扫描"）
func (r *Router) handleSoundSub(w http.ResponseWriter, req *http.Request) {
	sub := strings.TrimPrefix(req.URL.Path, "/api/sound/")
	switch {
	case sub == "plugin":
		ref, err := soundlib.ResolvePlugin(req.URL.Query().Get("id"))
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if ref.Format == soundlib.KindVST3 {
			sup := engine.Get()
			if sup == nil {
				writeError(w, 503, "VST3 引擎未启动")
				return
			}
			list, err := sup.ListPlugins()
			if err != nil {
				writeError(w, 502, err.Error())
				return
			}
			var matched []engine.PluginInfo
			for _, plugin := range list.Plugins {
				if strings.EqualFold(filepath.Clean(plugin.Path), filepath.Clean(ref.Path)) && (ref.ID == "" || plugin.ID == ref.ID || strings.HasSuffix(plugin.ID, "|"+ref.ID)) {
					matched = append(matched, plugin)
				}
			}
			if len(matched) != 1 {
				writeError(w, 400, "VST3 注册表引用未能唯一匹配已安装插件")
				return
			}
			ref.ID = matched[0].ID
			ref.Category = matched[0].Category
		}
		writeJSON(w, 200, ref)
	case sub == "sources":
		switch req.Method {
		case http.MethodGet:
			r.handleSoundSourcesList(w, req)
		case http.MethodPost:
			r.handleSoundSourceAdd(w, req)
		default:
			writeError(w, http.StatusMethodNotAllowed, "仅支持 GET / POST")
		}
	case strings.HasPrefix(sub, "sources/"):
		id := strings.TrimPrefix(sub, "sources/")
		if id == "" || strings.ContainsAny(id, `/\`) {
			writeError(w, http.StatusBadRequest, "无效的音源 id")
			return
		}
		switch req.Method {
		case http.MethodPut, http.MethodPost:
			r.handleSoundSourceUpdate(w, req, id)
		case http.MethodDelete:
			r.handleSoundSourceDelete(w, req, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "仅支持 PUT / DELETE")
		}
	case sub == "presets":
		r.handleSoundPresets(w, req)
	case sub == "data":
		src, err := soundlib.Get(req.URL.Query().Get("id"))
		if err != nil {
			writeError(w, 404, err.Error())
			return
		}
		if src.Kind != soundlib.KindSF2 || !src.Enabled {
			writeError(w, 403, "音源不可用或已禁用")
			return
		}
		path := soundlib.ResolvePath(src.Path)
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			writeError(w, 404, "音源文件不存在")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, req, path)
	case sub == "samples":
		r.handleSoundSamples(w, req)
	case sub == "import/sf2":
		r.handleSoundImportSF2(w, req)
	case sub == "reveal":
		r.handleSoundReveal(w, req)
	case sub == "migrate":
		r.handleSoundMigrate(w, req)
	default:
		writeError(w, http.StatusNotFound, "未知接口")
	}
}

// handleSoundSourcesList GET /api/sound/sources
func (r *Router) handleSoundSourcesList(w http.ResponseWriter, req *http.Request) {
	kind := strings.TrimSpace(req.URL.Query().Get("kind"))
	q := req.URL.Query().Get("q")
	list := soundlib.List(kind, q)
	writeJSON(w, http.StatusOK, map[string]any{
		"version": soundlib.RegistryVersion,
		"sources": list,
	})
}

// handleSoundSourceAdd POST /api/sound/sources
// 目前用于登记采样包目录（SF2 走 /api/sound/import/sf2，VST3/MM 走各自扫描/安装）
func (r *Router) handleSoundSourceAdd(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Kind   string   `json:"kind"`
		Name   string   `json:"name"`
		Path   string   `json:"path"`
		Dirs   []string `json:"dirs"`
		Tags   []string `json:"tags"`
		Vendor string   `json:"vendor"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的请求体")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = soundlib.KindSamplePack
	}

	switch kind {
	case soundlib.KindSamplePack:
		dirs := make([]string, 0, len(in.Dirs))
		if strings.TrimSpace(in.Path) != "" {
			dirs = append(dirs, in.Path)
		}
		dirs = append(dirs, in.Dirs...)
		if len(dirs) == 0 {
			writeError(w, http.StatusBadRequest, "目录路径不能为空")
			return
		}
		clean := make([]string, 0, len(dirs))
		for _, d := range dirs {
			abs := filepath.Clean(strings.TrimSpace(d))
			info, err := os.Stat(abs)
			if err != nil || !info.IsDir() {
				writeError(w, http.StatusBadRequest, "目录不存在或不可访问: "+d)
				return
			}
			clean = append(clean, abs)
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = filepath.Base(clean[0])
		}
		src, err := soundlib.Add(soundlib.Source{
			Kind:    kind,
			Name:    name,
			Vendor:  in.Vendor,
			Origin:  soundlib.OriginUser,
			Dirs:    clean,
			Tags:    in.Tags,
			Enabled: true,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "登记采样包失败", err)
			return
		}
		soundlib.SyncMaterialDirs()
		writeJSON(w, http.StatusOK, map[string]any{"source": src})

	default:
		writeError(w, http.StatusBadRequest, "该音源种类请通过导入/扫描接口添加")
	}
}

// handleSoundSourceUpdate PUT /api/sound/sources/{id}
// 只接受白名单字段，避免前端误传 path/kind 把条目指到别处
func (r *Router) handleSoundSourceUpdate(w http.ResponseWriter, req *http.Request, id string) {
	var in struct {
		Name    *string   `json:"name"`
		Vendor  *string   `json:"vendor"`
		Enabled *bool     `json:"enabled"`
		Dirs    *[]string `json:"dirs"`
		Tags    *[]string `json:"tags"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	var badDir string
	src, err := soundlib.Update(id, func(s *soundlib.Source) error {
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if name == "" {
				return errors.New("名称不能为空")
			}
			s.Name = name
		}
		if in.Vendor != nil {
			s.Vendor = strings.TrimSpace(*in.Vendor)
		}
		if in.Enabled != nil {
			s.Enabled = *in.Enabled
		}
		if in.Tags != nil {
			s.Tags = *in.Tags
		}
		if in.Dirs != nil {
			clean := make([]string, 0, len(*in.Dirs))
			for _, d := range *in.Dirs {
				abs := filepath.Clean(strings.TrimSpace(d))
				if abs == "" || abs == "." {
					continue
				}
				if info, err := os.Stat(abs); err != nil || !info.IsDir() {
					badDir = d
					return errors.New("目录不存在或不可访问")
				}
				clean = append(clean, abs)
			}
			s.Dirs = clean
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, soundlib.ErrNotFound):
			writeError(w, http.StatusNotFound, "音源不存在")
		case badDir != "":
			writeError(w, http.StatusBadRequest, "目录不存在或不可访问: "+badDir)
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	soundlib.SyncMaterialDirs()
	writeJSON(w, http.StatusOK, map[string]any{"source": src})
}

// handleSoundSourceDelete DELETE /api/sound/sources/{id}?file=1
// file=1 时连带删除磁盘文件（SF2 卸载语义）。只允许删音色库目录内的文件。
func (r *Router) handleSoundSourceDelete(w http.ResponseWriter, req *http.Request, id string) {
	view, err := soundlib.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "音源不存在")
		return
	}
	removed, err := soundlib.Delete(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "删除音源条目失败", err)
		return
	}
	soundlib.SyncMaterialDirs()

	deletedFile := false
	if strings.TrimSpace(req.URL.Query().Get("file")) == "1" && removed.Kind == soundlib.KindSF2 {
		abs := soundlib.ResolvePath(removed.Path)
		dirAbs, _ := filepath.Abs(soundlib.SoundFontDir())
		if p, aerr := filepath.Abs(abs); aerr == nil && isSubPath(p, dirAbs) {
			if rmErr := os.Remove(p); rmErr == nil {
				deletedFile = true
			} else if !os.IsNotExist(rmErr) {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": true, "source": view.Source, "deletedFile": false,
					"detail": "条目已删除，但磁盘文件删除失败: " + rmErr.Error(),
				})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "source": view.Source, "deletedFile": deletedFile,
	})
}

// handleSoundPresets GET /api/sound/presets?id=<sourceId>
// 直接读 SF2 的 pdta/phdr，不依赖浏览器端 IndexedDB 记录——
// 换机器/清缓存后预设列表依然可用（引擎模式发声只需要 bank:program）。
func (r *Router) handleSoundPresets(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimSpace(req.URL.Query().Get("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "缺少 id 参数")
		return
	}
	view, err := soundlib.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "音源不存在")
		return
	}
	if view.Kind != soundlib.KindSF2 {
		writeError(w, http.StatusBadRequest, "该音源不支持预设列举")
		return
	}
	if view.Runtime.Missing {
		writeError(w, http.StatusNotFound, "音色文件不存在")
		return
	}
	info, err := soundlib.ReadSF2Presets(view.Runtime.AbsPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "解析 SF2 预设失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          view.ID,
		"name":        view.Name,
		"presets":     info.Presets,
		"presetCount": info.PresetCount,
	})
}

// handleSoundSamples GET /api/sound/samples?pack=<id>&sub=<相对子路径>
// 采样包内容列举，路径校验沿用素材目录白名单（只列注册目录之内）。
func (r *Router) handleSoundSamples(w http.ResponseWriter, req *http.Request) {
	packID := strings.TrimSpace(req.URL.Query().Get("pack"))
	if packID == "" {
		writeError(w, http.StatusBadRequest, "缺少 pack 参数")
		return
	}
	view, err := soundlib.Get(packID)
	if err != nil || view.Kind != soundlib.KindSamplePack {
		writeError(w, http.StatusNotFound, "采样包不存在")
		return
	}
	sub := strings.TrimSpace(req.URL.Query().Get("sub"))
	if strings.Contains(sub, "..") || strings.ContainsAny(sub, "\x00") {
		writeError(w, http.StatusBadRequest, "非法的子路径")
		return
	}

	root := ""
	if len(view.Dirs) > 0 {
		root = soundlib.ResolvePath(view.Dirs[0])
	}
	if root == "" {
		writeError(w, http.StatusNotFound, "采样包未配置目录")
		return
	}
	full := root
	if sub != "" {
		full = filepath.Join(root, filepath.FromSlash(sub))
	}
	// 白名单：拼接后必须仍在注册目录之内
	if p, aerr := filepath.Abs(full); aerr != nil || !dirWithinAny(p, view.Dirs) {
		writeError(w, http.StatusForbidden, "路径不在采样包目录内")
		return
	}

	entries, err := os.ReadDir(full)
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取目录失败")
		return
	}
	type item struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size,omitempty"`
		Ext  string `json:"ext,omitempty"`
	}
	dirs := []item{}
	files := []item{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, item{Name: name, Type: "dir"})
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		mime, isAudio := arrangeAudioExt[ext]
		if !isAudio {
			continue
		}
		size := int64(0)
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
		}
		files = append(files, item{Name: name, Type: "file", Size: size, Ext: mime})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })

	writeJSON(w, http.StatusOK, map[string]any{
		"pack":  view.Source,
		"root":  root,
		"sub":   sub,
		"dirs":  dirs,
		"files": files,
	})
}

// dirWithinAny 校验绝对路径位于任一注册目录之内（防路径穿越）
func dirWithinAny(abs string, dirs []string) bool {
	for _, d := range dirs {
		base := soundlib.ResolvePath(d)
		if base == "" {
			continue
		}
		if b, err := filepath.Abs(base); err == nil && isSubPath(abs, b) {
			return true
		}
	}
	return false
}

// handleSoundImportSF2 POST /api/sound/import/sf2?name=xxx.sf2
// 与 /api/audio/soundfonts 共用同一套校验与落盘，差别只在响应形状：
// 本入口是"声音管理"的正式导入通道，返回入册后的条目供界面直接定位。
func (r *Router) handleSoundImportSF2(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	buf, uerr := readSF2Upload(req)
	if uerr != nil {
		writeError(w, uerr.status, uerr.detail)
		return
	}
	rawName := req.URL.Query().Get("name")
	dest, err := writeSF2File(rawName, buf)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存失败", err)
		return
	}
	src, created, err := soundlib.EnsureSF2Source(dest, sf2DisplayName(rawName))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "音色已保存，但登记音源失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved":  dest,
		"bytes":  len(buf),
		"source": src,
		"added":  created,
	})
}

// handleSoundReveal POST /api/sound/reveal {"id":"..."}
// 在资源管理器里定位音源：SF2 选中文件本身，采样包打开首个目录。
// 与知识库「📁 打开文件夹」同思路——用户要拿外部工具批量整理文件。
func (r *Router) handleSoundReveal(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil || strings.TrimSpace(in.ID) == "" {
		writeError(w, http.StatusBadRequest, "缺少音源 id")
		return
	}
	view, err := soundlib.Get(in.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "音源不存在")
		return
	}

	var target string
	selectFile := false
	switch view.Kind {
	case soundlib.KindSF2:
		abs := soundlib.ResolvePath(view.Path)
		info, statErr := os.Stat(abs)
		if statErr != nil || info.IsDir() {
			writeError(w, http.StatusNotFound, "音色文件不存在")
			return
		}
		target, selectFile = abs, true
	case soundlib.KindSamplePack:
		for _, d := range view.Dirs {
			abs := soundlib.ResolvePath(d)
			if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
				target = abs
				break
			}
		}
		if target == "" {
			writeError(w, http.StatusNotFound, "采样包目录不存在或不可访问")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "该音源类型不支持定位文件")
		return
	}

	// explorer /select,"文件" 会选中该文件；目录直接打开
	args := []string{target}
	if selectFile {
		args = []string{"/select,", target}
	}
	if err := exec.Command("explorer", args...).Start(); err != nil {
		writeErr(w, http.StatusInternalServerError, "打开位置失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": target})
}

// handleSoundMigrate POST /api/sound/migrate// 重新扫描 Library/soundfonts 与素材目录并入册（用户手动拷入文件后不必重启）
func (r *Router) handleSoundMigrate(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	res, err := soundlib.Migrate()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "扫描音源失败", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"result":  res,
		"sources": soundlib.List("", ""),
	})
}
