package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"melodymate/internal/config"
	"melodymate/internal/soundlib"
)

// withTempSoundRoot 把数据根换绑到临时目录（注册表/音色库/settings 都随之）
func withTempSoundRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldRoot, oldLib, oldUser, oldSettings := config.ProjectRoot, config.LibraryDir, config.LibraryUserDir, config.SettingsFile
	config.ProjectRoot = root
	config.LibraryDir = filepath.Join(root, "Library")
	config.LibraryUserDir = filepath.Join(config.LibraryDir, "user")
	config.SettingsFile = filepath.Join(root, "settings.json")
	t.Cleanup(func() {
		config.ProjectRoot, config.LibraryDir, config.LibraryUserDir, config.SettingsFile = oldRoot, oldLib, oldUser, oldSettings
	})
	return root
}

// testSF2 造一个结构合法的最小 SF2（RIFF/sfbk + INFO/INAM + pdta/PHDR）
func testSF2(fontName string, presetCount int) []byte {
	chunk := func(id string, data []byte) []byte {
		out := make([]byte, 8, 8+len(data)+1)
		copy(out[0:4], id)
		binary.LittleEndian.PutUint32(out[4:8], uint32(len(data)))
		out = append(out, data...)
		if len(data)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}
	list := func(typ string, subs ...[]byte) []byte {
		body := append([]byte(typ), nil...)
		for _, s := range subs {
			body = append(body, s...)
		}
		return chunk("LIST", body)
	}

	inam := append([]byte(fontName), 0)
	if len(inam)%2 == 1 {
		inam = append(inam, 0)
	}
	info := list("INFO", chunk("INAM", inam))

	// PHDR：presetCount 条 + 1 条 EOP
	pdta := make([]byte, 0, (presetCount+1)*38)
	for i := 0; i <= presetCount; i++ {
		rec := make([]byte, 38)
		copy(rec[0:20], "P")
		binary.LittleEndian.PutUint16(rec[20:22], uint16(i))
		binary.LittleEndian.PutUint16(rec[22:24], 0)
		pdta = append(pdta, rec...)
	}
	body := append(append([]byte("sfbk"), info...), list("pdta", chunk("PHDR", pdta))...)
	out := make([]byte, 8, 8+len(body))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(body)))
	return append(out, body...)
}

// soundRouter 只挂 /api/sound/ 的最小路由器（不经 updateGate 等中间件）
func soundRouter() *Router {
	r := &Router{mux: http.NewServeMux()}
	r.mux.HandleFunc("/api/sound/", r.handleSoundSub)
	return r
}

func doSound(t *testing.T, r *Router, method, target string, body []byte) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rd)
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

func TestSoundSourcesListAndSamplePackCRUD(t *testing.T) {
	root := withTempSoundRoot(t)
	r := soundRouter()

	dir := filepath.Join(root, "samples", "kit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kick.wav"), []byte("wav"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 空注册表
	code, out := doSound(t, r, http.MethodGet, "/api/sound/sources", nil)
	if code != 200 {
		t.Fatalf("列举失败: %d %v", code, out)
	}
	if srcs, _ := out["sources"].([]any); len(srcs) != 0 {
		t.Fatalf("初始应为空: %v", out)
	}

	// 新增采样包
	payload, _ := json.Marshal(map[string]any{"kind": "samplepack", "name": "808 鼓组", "dirs": []string{dir}})
	code, out = doSound(t, r, http.MethodPost, "/api/sound/sources", payload)
	if code != 200 {
		t.Fatalf("新增采样包失败: %d %v", code, out)
	}
	src := out["source"].(map[string]any)
	id := src["id"].(string)
	if !strings.HasPrefix(id, "pack_") {
		t.Fatalf("采样包 id 前缀不对: %s", id)
	}
	// 素材目录镜像应同步
	if got := config.LoadSettings().MaterialDirs; len(got) != 1 {
		t.Fatalf("采样包应写入素材目录镜像: %v", got)
	}

	// 列举应带磁盘实况
	code, out = doSound(t, r, http.MethodGet, "/api/sound/sources?kind=samplepack", nil)
	if code != 200 {
		t.Fatal(code)
	}
	srcs := out["sources"].([]any)
	if len(srcs) != 1 {
		t.Fatalf("应有 1 个采样包: %v", srcs)
	}
	rt := srcs[0].(map[string]any)["runtime"].(map[string]any)
	if n, _ := rt["sampleFiles"].(float64); n != 1 {
		t.Fatalf("应统计到 1 个音频文件: %v", rt)
	}

	// 停用 → 素材镜像清空
	payload, _ = json.Marshal(map[string]any{"enabled": false})
	code, out = doSound(t, r, http.MethodPut, "/api/sound/sources/"+id, payload)
	if code != 200 {
		t.Fatalf("停用失败: %d %v", code, out)
	}
	if got := config.LoadSettings().MaterialDirs; len(got) != 0 {
		t.Fatalf("停用后素材镜像应为空: %v", got)
	}

	// 改名
	payload, _ = json.Marshal(map[string]any{"name": "改名后"})
	code, out = doSound(t, r, http.MethodPut, "/api/sound/sources/"+id, payload)
	if code != 200 {
		t.Fatal(code, out)
	}
	if out["source"].(map[string]any)["name"] != "改名后" {
		t.Fatalf("改名未生效: %v", out)
	}

	// 空名称应被拒
	payload, _ = json.Marshal(map[string]any{"name": "   "})
	if code, _ = doSound(t, r, http.MethodPut, "/api/sound/sources/"+id, payload); code != http.StatusBadRequest {
		t.Fatalf("空名称应 400，got %d", code)
	}

	// 删除
	if code, out = doSound(t, r, http.MethodDelete, "/api/sound/sources/"+id, nil); code != 200 {
		t.Fatalf("删除失败: %d %v", code, out)
	}
	if code, _ = doSound(t, r, http.MethodGet, "/api/sound/sources", nil); code != 200 {
		t.Fatal(code)
	}
}

func TestSoundAddRejectsMissingDir(t *testing.T) {
	withTempSoundRoot(t)
	r := soundRouter()
	payload, _ := json.Marshal(map[string]any{"kind": "samplepack", "dirs": []string{filepath.Join(t.TempDir(), "nope")}})
	if code, out := doSound(t, r, http.MethodPost, "/api/sound/sources", payload); code != http.StatusBadRequest {
		t.Fatalf("不存在的目录应 400: %d %v", code, out)
	}
}

func TestSoundImportSF2RegistersAndLists(t *testing.T) {
	withTempSoundRoot(t)
	r := soundRouter()

	raw := testSF2("My Font", 2)
	code, out := doSound(t, r, http.MethodPost, "/api/sound/import/sf2?name=My%20Font.sf2", raw)
	if code != 200 {
		t.Fatalf("导入失败: %d %v", code, out)
	}
	if added, _ := out["added"].(bool); !added {
		t.Fatalf("首次导入应新建条目: %v", out)
	}
	dest := out["saved"].(string)
	if filepath.Dir(dest) != soundlib.SoundFontDir() {
		t.Fatalf("落盘位置不对: %s", dest)
	}
	src := out["source"].(map[string]any)
	// 落盘名被消毒（空格→下划线），展示名保留用户原名
	if src["kind"] != "sf2" || src["path"] != "Library/soundfonts/My_Font.sf2" || src["name"] != "My Font" {
		t.Fatalf("条目内容不对: %v", src)
	}

	// 重复导入同一文件：仍应只有一条（path 即身份）
	code, out = doSound(t, r, http.MethodPost, "/api/sound/import/sf2?name=My%20Font.sf2", raw)
	if code != 200 {
		t.Fatal(code, out)
	}
	if added, _ := out["added"].(bool); added {
		t.Fatal("重复导入不应新建条目")
	}
	_, out = doSound(t, r, http.MethodGet, "/api/sound/sources?kind=sf2", nil)
	if got := len(out["sources"].([]any)); got != 1 {
		t.Fatalf("应只有 1 条 SF2: %d", got)
	}
}

func TestSoundImportSF2RejectsBadPayload(t *testing.T) {
	withTempSoundRoot(t)
	r := soundRouter()

	// 非 SF2
	if code, _ := doSound(t, r, http.MethodPost, "/api/sound/import/sf2?name=x.sf2", []byte("hello world!!")); code != http.StatusBadRequest {
		t.Fatalf("非 SF2 应 400, got %d", code)
	}
	// 头部合法但被截断（RIFF size 与实收不符）
	full := testSF2("Cut", 3)
	if code, _ := doSound(t, r, http.MethodPost, "/api/sound/import/sf2?name=cut.sf2", full[:len(full)-40]); code != http.StatusBadRequest {
		t.Fatalf("截断文件应 400, got %d", code)
	}
	// 都不该落盘
	entries, _ := os.ReadDir(soundlib.SoundFontDir())
	if len(entries) != 0 {
		t.Fatalf("校验失败不应落盘: %v", entries)
	}
}

func TestSoundPresetsEndpoint(t *testing.T) {
	withTempSoundRoot(t)
	r := soundRouter()

	raw := testSF2("My Font", 3)
	_, out := doSound(t, r, http.MethodPost, "/api/sound/import/sf2?name=My%20Font.sf2", raw)
	id := out["source"].(map[string]any)["id"].(string)

	code, got := doSound(t, r, http.MethodGet, "/api/sound/presets?id="+id, nil)
	if code != 200 {
		t.Fatalf("预设列举失败: %d %v", code, got)
	}
	presets := got["presets"].([]any)
	if len(presets) != 3 {
		t.Fatalf("应有 3 个预设: %v", presets)
	}
	if p0 := presets[0].(map[string]any); p0["id"] != "0:0" {
		t.Fatalf("预设 id 应为 bank:program: %v", p0)
	}

	// 不存在的 id
	if code, _ := doSound(t, r, http.MethodGet, "/api/sound/presets?id=nope", nil); code != http.StatusNotFound {
		t.Fatalf("未知 id 应 404, got %d", code)
	}
	// 非法 kind（采样包没有预设）
	dir := t.TempDir()
	payload, _ := json.Marshal(map[string]any{"kind": "samplepack", "dirs": []string{dir}})
	_, out = doSound(t, r, http.MethodPost, "/api/sound/sources", payload)
	packID := out["source"].(map[string]any)["id"].(string)
	if code, _ := doSound(t, r, http.MethodGet, "/api/sound/presets?id="+packID, nil); code != http.StatusBadRequest {
		t.Fatalf("采样包列举预设应 400, got %d", code)
	}
}

func TestSoundSamplesListingAndTraversalGuard(t *testing.T) {
	withTempSoundRoot(t)
	r := soundRouter()

	root := filepath.Join(t.TempDir(), "pack")
	sub := filepath.Join(root, "kick")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "snare.wav"), []byte("wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "kick_808.wav"), []byte("wav"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{"kind": "samplepack", "name": "kit", "dirs": []string{root}})
	_, out := doSound(t, r, http.MethodPost, "/api/sound/sources", payload)
	id := out["source"].(map[string]any)["id"].(string)

	code, out := doSound(t, r, http.MethodGet, "/api/sound/samples?pack="+id, nil)
	if code != 200 {
		t.Fatalf("列举失败: %d %v", code, out)
	}
	if files := out["files"].([]any); len(files) != 1 {
		t.Fatalf("顶层应只列音频文件（txt 被过滤）: %v", files)
	}
	if dirs := out["dirs"].([]any); len(dirs) != 1 {
		t.Fatalf("应列出 1 个子目录: %v", dirs)
	}

	// 子目录
	code, out = doSound(t, r, http.MethodGet, "/api/sound/samples?pack="+id+"&sub=kick", nil)
	if code != 200 {
		t.Fatal(code, out)
	}
	if files := out["files"].([]any); len(files) != 1 {
		t.Fatalf("子目录应列 1 个文件: %v", files)
	}

	// 路径穿越必须被挡
	if code, _ := doSound(t, r, http.MethodGet, "/api/sound/samples?pack="+id+"&sub=../..", nil); code == 200 {
		t.Fatal("路径穿越应被拒绝")
	}
	// 未知采样包
	if code, _ := doSound(t, r, http.MethodGet, "/api/sound/samples?pack=pack_nope", nil); code != http.StatusNotFound {
		t.Fatalf("未知采样包应 404, got %d", code)
	}
}

func TestSoundSourceDeleteWithFileRemoval(t *testing.T) {
	withTempSoundRoot(t)
	r := soundRouter()

	raw := testSF2("Doomed", 1)
	_, out := doSound(t, r, http.MethodPost, "/api/sound/import/sf2?name=Doomed.sf2", raw)
	src := out["source"].(map[string]any)
	id := src["id"].(string)
	dest := out["saved"].(string)

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("文件应已落盘: %v", err)
	}
	code, out := doSound(t, r, http.MethodDelete, "/api/sound/sources/"+id+"?file=1", nil)
	if code != 200 {
		t.Fatalf("删除失败: %d %v", code, out)
	}
	if del, _ := out["deletedFile"].(bool); !del {
		t.Fatalf("应连带删除磁盘文件: %v", out)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("磁盘文件应已删除")
	}
}

func TestSoundMigrateEndpointAbsorbsDiskFonts(t *testing.T) {
	root := withTempSoundRoot(t)
	r := soundRouter()

	fontDir := filepath.Join(root, "Library", "soundfonts")
	if err := os.MkdirAll(fontDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fontDir, "Existing.sf2"), testSF2("Existing", 1), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := doSound(t, r, http.MethodPost, "/api/sound/migrate", nil)
	if code != 200 {
		t.Fatalf("扫描失败: %d %v", code, out)
	}
	res := out["result"].(map[string]any)
	if n, _ := res["soundFonts"].(float64); n != 1 {
		t.Fatalf("应入册 1 个 SF2: %v", res)
	}
	if got := len(out["sources"].([]any)); got != 1 {
		t.Fatalf("应返回 1 条注册表条目: %d", got)
	}
	// 幂等
	_, out = doSound(t, r, http.MethodPost, "/api/sound/migrate", nil)
	if n := out["result"].(map[string]any)["soundFonts"].(float64); n != 0 {
		t.Fatalf("二次扫描不应重复入册: %v", out)
	}
}

// /api/audio/soundfonts 是兼容入口：上传同样要进注册表，
// 否则旧界面传的音色在声音管理/机架里看不到
func TestLegacySoundfontUploadRegistersSource(t *testing.T) {
	withTempSoundRoot(t)
	r := &Router{mux: http.NewServeMux()}
	r.mux.HandleFunc("/api/audio/", r.handleAudioSub)

	raw := testSF2("Legacy", 1)
	code, out := doSound(t, r, http.MethodPost, "/api/audio/soundfonts?name=Legacy.sf2", raw)
	if code != 200 {
		t.Fatalf("旧入口上传失败: %d %v", code, out)
	}
	src, ok := out["source"].(map[string]any)
	if !ok {
		t.Fatalf("旧入口应返回注册表条目: %v", out)
	}
	if src["kind"] != "sf2" {
		t.Fatalf("条目种类不对: %v", src)
	}
	if got := soundlib.List(soundlib.KindSF2, ""); len(got) != 1 {
		t.Fatalf("上传后注册表应有 1 条: %d", len(got))
	}

	// 旧入口的 DELETE 也要清理注册表条目，避免"删了还在声音管理里"
	if code, out = doSound(t, r, http.MethodDelete, "/api/audio/soundfonts?name=Legacy.sf2", nil); code != 200 {
		t.Fatalf("旧入口删除失败: %d %v", code, out)
	}
	if unreg, _ := out["unregistered"].(bool); !unreg {
		t.Fatalf("旧入口删除应撤销注册表条目: %v", out)
	}
	if got := soundlib.List(soundlib.KindSF2, ""); len(got) != 0 {
		t.Fatalf("删除后注册表应为空: %+v", got)
	}
}

// 经完整 Router（含 Host/Origin 校验、请求体上限、更新闸门等中间件）
// 走一遍声音管理主链路：新增接口必须真的注册进 mux，且限流档位不误伤上传。
func TestSoundRoutesThroughFullRouter(t *testing.T) {
	root := withTempSoundRoot(t)
	r := NewRouter(nil, nil)

	call := func(method, target string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewReader(body))
		req.Host = "127.0.0.1:7860"
		req.Header.Set("Origin", "http://127.0.0.1:7860")
		if testAuthToken != "" {
			req.Header.Set("X-MM-Token", testAuthToken)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON: %s", w.Body.String())
		}
		return out
	}

	// 空注册表
	if w := call(http.MethodGet, "/api/sound/sources", nil); w.Code != 200 {
		t.Fatalf("列举失败: %d %s", w.Code, w.Body.String())
	}

	// 导入 SF2（走通 288MB 限流档位 + handler）
	w := call(http.MethodPost, "/api/sound/import/sf2?name=Full.sf2", testSF2("Full Font", 2))
	if w.Code != 200 {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}
	imp := decode(w)
	src := imp["source"].(map[string]any)
	id := src["id"].(string)

	// 预设可读
	w = call(http.MethodGet, "/api/sound/presets?id="+id, nil)
	if w.Code != 200 {
		t.Fatalf("预设列举失败: %d %s", w.Code, w.Body.String())
	}
	if n := len(decode(w)["presets"].([]any)); n != 2 {
		t.Fatalf("应有 2 个预设: %d", n)
	}

	// 采样包 → 素材目录镜像对 /api/arrangement/dirs 可见（老素材树仍能读到）
	packDir := filepath.Join(root, "samples", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"kind": "samplepack", "name": "包", "dirs": []string{packDir}})
	if w = call(http.MethodPost, "/api/sound/sources", payload); w.Code != 200 {
		t.Fatalf("新增采样包失败: %d %s", w.Code, w.Body.String())
	}
	w = call(http.MethodGet, "/api/arrangement/dirs", nil)
	if w.Code != 200 {
		t.Fatalf("素材目录读取失败: %d", w.Code)
	}
	dirs, _ := decode(w)["dirs"].([]any)
	if len(dirs) != 1 || !sameDirString(dirs[0].(string), packDir) {
		t.Fatalf("采样包应同步到素材目录: %v", dirs)
	}

	// 删除 SF2 条目（连带磁盘文件）后注册表清空
	if w = call(http.MethodDelete, "/api/sound/sources/"+id+"?file=1", nil); w.Code != 200 {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
	if got := soundlib.List(soundlib.KindSF2, ""); len(got) != 0 {
		t.Fatalf("删除后应为空: %+v", got)
	}

	// 未知子路径仍要 404（路由收敛，不漏到静态资源）
	if w = call(http.MethodGet, "/api/sound/nope", nil); w.Code != http.StatusNotFound {
		t.Fatalf("未知接口应 404: %d", w.Code)
	}
}

func sameDirString(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
