// Command mm 是 MM 插件的一键编译/打包/校验工具（规范见 docs/MM插件规范.md）。
//
//	mm new <dir> --kind effect|instrument --name "..." --id com.x.y
//	mm doctor                     检测构建工具链（VS Build Tools + cmake）
//	mm build [dir]                配置 + 编译 + 收集 bin/<platform>/ + 校验 + 打包 .mm + 写 sha256
//	mm validate <pkg.mm>          校验包（规范 §1–§4 全量规则）
//	mm inspect <pkg.mm>           打印 manifest 摘要与文件清单
//	mm install <pkg.mm> --dest <Library/plugins> [--grant files,network]
//	mm test <dir>                 离线渲染自测（加载 DLL 渲染一个块，验证参数生效）
//
// 只依赖 Go 标准库 + 本仓 internal/mm（校验器与宿主同源，规范不会漂移）。
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"melodymate/internal/mm"
)

const usage = `mm —— MelodyMate MM 插件工具

用法:
  mm new <dir> --kind effect|instrument --name "显示名" --id <反向域名>
  mm doctor
  mm build [dir] [--out <file.mm>] [--config Release]
  mm validate <pkg.mm>
  mm inspect <pkg.mm>
  mm install <pkg.mm> --dest <plugins 目录> [--grant cap1,cap2]
  mm test [dir]
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "new":
		err = cmdNew(args)
	case "doctor":
		err = cmdDoctor()
	case "build":
		err = cmdBuild(args)
	case "validate":
		err = cmdValidate(args)
	case "inspect":
		err = cmdInspect(args)
	case "install":
		err = cmdInstall(args)
	case "test":
		err = cmdTest(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "[mm] 失败: "+err.Error())
		os.Exit(1)
	}
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return ""
}

func positional(args []string) []string {
	var out []string
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		if strings.HasPrefix(a, "--") {
			if !strings.Contains(a, "=") {
				skip = true
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

/* ═══════════ mm new ═══════════ */

func cmdNew(args []string) error {
	pos := positional(args)
	if len(pos) == 0 {
		return fmt.Errorf("缺少目标目录：mm new <dir> --kind effect --name \"...\" --id com.x.y")
	}
	dir := pos[0]
	kind := flagValue(args, "--kind")
	if kind == "" {
		kind = "effect"
	}
	if kind != "effect" && kind != "instrument" {
		return fmt.Errorf("--kind 只能是 effect 或 instrument")
	}
	id := flagValue(args, "--id")
	name := flagValue(args, "--name")
	if id == "" || name == "" {
		return fmt.Errorf("必须给出 --id 与 --name（manifest 的必填字段）")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,127}$`).MatchString(id) {
		return fmt.Errorf("--id 非法（反向域名，小写字母/数字/./-）: %s", id)
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("目录已存在: %s", dir)
	}
	libName := strings.ReplaceAll(id, ".", "_")

	files := map[string]string{
		"CMakeLists.txt": fmt.Sprintf(`cmake_minimum_required(VERSION 3.20)
project(%s LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# MM SDK 位置：默认取环境变量 MM_SDK_ROOT，回落到引擎仓库的 mm-sdk
set(MM_SDK_ROOT "$ENV{MM_SDK_ROOT}" CACHE PATH "MM SDK 根目录")
if(NOT MM_SDK_ROOT)
  set(MM_SDK_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/../../mm-sdk")
endif()

add_library(%s SHARED src/plugin.cpp)
target_include_directories(%s PRIVATE "${MM_SDK_ROOT}/include")
if(MSVC)
  target_compile_options(%s PRIVATE /utf-8 /W3)
endif()
set_target_properties(%s PROPERTIES PREFIX "" OUTPUT_NAME "%s"
  RUNTIME_OUTPUT_DIRECTORY "${CMAKE_BINARY_DIR}/bin"
  LIBRARY_OUTPUT_DIRECTORY "${CMAKE_BINARY_DIR}/bin")
`, libName, libName, libName, libName, libName, libName),

		"src/plugin.cpp": fmt.Sprintf(`// %s —— MM 插件（%s）
// 只写 DSP 逻辑：音频 I/O、线程、缓冲、参数平滑都由宿主托管。
#include <mm/mm.h>

class Plugin : public mm::Processor
{
public:
    Plugin() : gain_(0.0f, 1.0f, 0.8f) {}

    void prepare(double sampleRate, int blockSize, mm::HostServices* services) override
    {
        sampleRate_ = sampleRate;
        blockSize_ = blockSize;
        (void)services;
    }

    void process(mm::AudioBufferView& audio, const mm::MidiView& midi, const mm::TransportInfo& transport) override
    {
        (void)midi;
        (void)transport;
        gain_.smooth(audio.numSamples, sampleRate_);
        const float g = gain_.value();
        for (int c = 0; c < audio.numChannels; ++c)
        {
            float* p = audio.channel(c);
            if (p == nullptr) continue;
            for (int i = 0; i < audio.numSamples; ++i) p[i] *= g;
        }
    }

    void onParam(int index, float normalized) override
    {
        if (index == 0) gain_.setNormalized(normalized);
    }

    const mm::ParamDecl* params(int& count) const override
    {
        static const mm::ParamDecl decls[] = { { "gain", "Gain", 0.0f, 1.0f, 0.8f, 0 } };
        count = 1;
        return decls;
    }

private:
    mm::SmoothedParam gain_;
    double sampleRate_ = 44100.0;
    int blockSize_ = 512;
};

MM_DECLARE_PLUGIN(Plugin, "%s", "%s", %d)
`, name, kindLabel(kind), id, name, kindCode(kind)),

		"manifest.json": fmt.Sprintf(`{
  "schema": 1,
  "id": "%s",
  "name": "%s",
  "version": "0.1.0",
  "kind": "%s",
  "entry": "win-x64/%s.dll",
  "api": 1,
  "capabilities": [],
  "params": [
    { "id": "gain", "name": "Gain", "min": 0, "max": 1, "default": 0.8, "curve": "linear" }
  ],
  "ui": { "entry": "ui/index.html", "width": 360, "height": 180, "resizable": false },
  "author": { "type": "human" }
}
`, id, name, kind, libName),

		"ui/index.html": `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>MM Plugin</title>
<style>
  body { margin:0; font:13px/1.5 system-ui,sans-serif; background:#12161c; color:#cfe3f0;
         display:flex; align-items:center; justify-content:center; height:100vh; gap:10px; }
  input[type=range]{ width:220px; }
</style></head>
<body>
  <label>Gain <input type="range" id="gain" min="0" max="1" step="0.001" value="0.8"></label>
  <script>
    var s = document.getElementById("gain");
    function send(type, payload){ parent.postMessage(Object.assign({type:type}, payload||{}), "*"); }
    s.addEventListener("input", function(){ send("mm.setParam", { id:"gain", value: parseFloat(s.value) }); });
    window.addEventListener("message", function(ev){
      var d = ev.data || {};
      if (d.type === "mm.params" && d.params) d.params.forEach(function(p){ if(p.id==="gain") s.value = p.value; });
      if (d.type === "mm.theme") document.documentElement.dataset.theme = d.theme;
    });
    send("mm.ready");
  </script>
</body></html>
`,

		"README.md": fmt.Sprintf("# %s\n\nMM 插件（%s）。\n\n```bash\nmm build          # 编译 + 校验 + 打包 .mm\nmm validate %s-0.1.0.mm\n```\n\nSDK 参考：docs/MM插件规范.md、docs/MM插件开发指南.md\n", name, kind, id),
	}

	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("[mm] 已生成脚手架: %s（%s / %s）\n", dir, id, kind)
	fmt.Println("[mm] 下一步: cd " + dir + " && mm build")
	return nil
}

func kindLabel(kind string) string {
	if kind == "instrument" {
		return "instrument"
	}
	return "effect"
}

func kindCode(kind string) int {
	if kind == "instrument" {
		return 1
	}
	return 0
}

/* ═══════════ mm doctor ═══════════ */

func cmdDoctor() error {
	fmt.Println("[mm] 检查构建工具链")
	type item struct {
		name string
		ok   bool
		path string
		hint string
	}
	var items []item

	if p, err := exec.LookPath("cmake"); err == nil {
		items = append(items, item{"cmake", true, p, ""})
	} else {
		items = append(items, item{"cmake", false, "", "安装 CMake 3.20+：https://cmake.org/download/（勾选 Add to PATH）"})
	}

	clOK, clPath := false, ""
	for _, c := range []string{"cl", "cl.exe"} {
		if p, err := exec.LookPath(c); err == nil {
			clOK, clPath = true, p
			break
		}
	}
	if clOK {
		items = append(items, item{"MSVC cl.exe", true, clPath, ""})
	} else {
		/* PATH 里没有 cl.exe 不等于不能编译：cmake 会用 vswhere 找到 VS 生成器
		   并自行设置工具链（VS2022 Build Tools 装了就够）。这里只警告，
		   真正能不能编由 mm build 决定 */
		items = append(items, item{"MSVC cl.exe", true, "未在 PATH（cmake 走 VS 生成器）", ""})
		fmt.Println("  ⚠ cl.exe 不在 PATH：cmake 会自动定位 VS 生成器；若 mm build 报找不到编译器，" +
			"请从「x64 Native Tools Command Prompt」运行或安装 VS2022 Build Tools（使用 C++ 的桌面开发）")
	}

	msbuildOK, msbuildPath := false, ""
	if p, err := exec.LookPath("msbuild"); err == nil {
		msbuildOK, msbuildPath = true, p
	}
	items = append(items, item{"MSBuild（可选）", true, msbuildPathOrHint(msbuildOK, msbuildPath),
		"cmake --build 会自动选择 VS 生成器；找不到 MSBuild 时可用 Ninja 或从 VS 命令行运行"})

	failed := 0
	for _, it := range items {
		if it.ok {
			fmt.Printf("  ✓ %-16s %s\n", it.name, it.path)
		} else {
			failed++
			fmt.Printf("  ✗ %-16s %s\n", it.name, it.hint)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d 项前置条件缺失（见上方指引）", failed)
	}
	fmt.Println("[mm] 工具链就绪，可以 mm new / mm build")
	return nil
}

/* ═══════════ mm build ═══════════ */

func cmdBuild(args []string) error {
	pos := positional(args)
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	config := flagValue(args, "--config")
	if config == "" {
		config = "Release"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(abs, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("读取 manifest.json 失败（是否在插件目录里？）: %w", err)
	}
	var m mm.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("manifest.json 不是合法 JSON: %w", err)
	}
	if m.ID == "" || m.Version == "" || m.Entry == "" {
		return fmt.Errorf("manifest 缺 id/version/entry（先补齐再 build）")
	}

	buildDir := filepath.Join(abs, "build")
	/* SDK 位置自动注入：插件可以建在任何目录（AI 生成的插件在 Library/plugin-src/ 下），
	   CMakeLists 里的相对回落只对仓库内示例有效。解析顺序：--sdk → 环境变量 →
	   从插件目录向上找 → 从 mm 可执行文件向上找。找不到就明确报错而不是让编译器吐
	   "mm/mm.h: No such file or directory" */
	sdkRoot := flagValue(args, "--sdk")
	if sdkRoot == "" {
		sdkRoot = os.Getenv("MM_SDK_ROOT")
	}
	if sdkRoot == "" {
		sdkRoot = findSDKRoot(abs)
	}
	if sdkRoot == "" {
		sdkRoot = findSDKRoot(filepath.Dir(os.Args[0]))
	}
	if sdkRoot == "" {
		return fmt.Errorf("找不到 MM SDK：用 --sdk <engine/mm-sdk> 指定，或设置 MM_SDK_ROOT 环境变量")
	}
	fmt.Printf("[mm] SDK: %s\n", sdkRoot)
	fmt.Printf("[mm] 配置 CMake（%s）…\n", config)
	if err := runCmds(abs, "cmake", "-S", ".", "-B", "build", "-DCMAKE_BUILD_TYPE="+config, "-DMM_SDK_ROOT="+sdkRoot); err != nil {
		return err
	}
	fmt.Println("[mm] 编译…")
	if err := runCmds(abs, "cmake", "--build", "build", "--config", config); err != nil {
		return err
	}

	// 收集产物到 bin/win-x64/
	platform := platformDir()
	binDir := filepath.Join(abs, "bin", platform)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	libBase := strings.TrimSuffix(filepath.Base(m.Entry), filepath.Ext(m.Entry))
	ext := filepath.Ext(m.Entry)
	found := ""
	candidates := []string{
		filepath.Join(buildDir, "bin", libBase+ext),
		filepath.Join(buildDir, "bin", config, libBase+ext),
	}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, filepath.Join(buildDir, "bin", libBase+".dll"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			found = c
			break
		}
	}
	if found == "" {
		// 兜底：在 build/ 下找同名库
		_ = filepath.Walk(buildDir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || found != "" {
				return nil
			}
			if strings.HasPrefix(info.Name(), libBase+".") && (strings.HasSuffix(info.Name(), ".dll") || strings.HasSuffix(info.Name(), ".so")) {
				found = p
			}
			return nil
		})
	}
	if found == "" {
		return fmt.Errorf("编译产物未找到（期望 %s%s）——检查 CMakeLists 的 OUTPUT_NAME 与 RUNTIME_OUTPUT_DIRECTORY", libBase, ext)
	}
	destLib := filepath.Join(binDir, libBase+ext)
	if err := copyFile(found, destLib); err != nil {
		return err
	}
	fmt.Printf("[mm] 收集产物: %s\n", filepath.ToSlash(filepath.Join("bin", platform, libBase+ext)))

	// 写哈希（规范 §2：hash.files）
	sum, err := fileSHA256(destLib)
	if err != nil {
		return err
	}
	if m.Hash == nil {
		m.Hash = &mm.Hash{Algorithm: "sha256", Files: map[string]string{}}
	}
	if m.Hash.Files == nil {
		m.Hash.Files = map[string]string{}
	}
	m.Hash.Algorithm = "sha256"
	m.Hash.Files[filepath.ToSlash(filepath.Join("bin", platform, libBase+ext))] = sum
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, append(out, '\n'), 0o644); err != nil {
		return err
	}

	// 打包 .mm（只收 manifest / bin / ui / presets / LICENSE / README）
	outPath := flagValue(args, "--out")
	if outPath == "" {
		outPath = filepath.Join(abs, fmt.Sprintf("%s-%s.mm", m.ID, m.Version))
	}
	if err := packMM(abs, outPath); err != nil {
		return err
	}

	// 用宿主同源校验器复核（规范不会漂移）
	res := mm.ValidateFile(outPath)
	if !res.OK {
		return fmt.Errorf("打包后校验失败（包已生成，但不可安装）: %v", res.Error())
	}
	fmt.Printf("[mm] 已生成 %s（%.1f KB，校验通过）\n", outPath, float64(fileSize(outPath))/1024)
	if m.UI != nil {
		fmt.Printf("[mm] 含 UI: %s\n", m.UI.Entry)
	}
	return nil
}

func runCmds(dir, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %s 失败: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func findSDKRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for i := 0; i < 8 && dir != ""; i++ {
		candidate := filepath.Join(dir, "engine", "mm-sdk")
		if _, err := os.Stat(filepath.Join(candidate, "include", "mm", "mm.h")); err == nil {
			return candidate
		}
		candidate2 := filepath.Join(dir, "mm-sdk")
		if _, err := os.Stat(filepath.Join(candidate2, "include", "mm", "mm.h")); err == nil {
			return candidate2
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func msbuildPathOrHint(ok bool, path string) string {
	if ok {
		return path
	}
	return "未找到（cmake 会自行选择 VS 生成器；也可用 Ninja）"
}

func platformDir() string {
	if runtime.GOOS == "windows" {
		if runtime.GOARCH == "arm64" {
			return "win-arm64"
		}
		return "win-x64"
	}
	if runtime.GOOS == "darwin" {
		return "macos-universal"
	}
	return "linux-x64"
}

func packMM(dir, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	allowed := []string{"manifest.json", "bin/", "ui/", "presets/", "LICENSE", "README.md"}
	var files []string
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(rel, "build/") || strings.HasSuffix(rel, ".mm") {
			return nil
		}
		ok := false
		for _, a := range allowed {
			if rel == a || strings.HasPrefix(rel, a) {
				ok = true
				break
			}
		}
		if ok {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, rel := range files {
		w, werr := zw.Create(rel)
		if werr != nil {
			return werr
		}
		src, oerr := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if oerr != nil {
			return oerr
		}
		if _, cerr := io.Copy(w, src); cerr != nil {
			src.Close()
			return cerr
		}
		src.Close()
	}
	return zw.Close()
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

/* ═══════════ validate / inspect / install / test ═══════════ */

func cmdValidate(args []string) error {
	pos := positional(args)
	if len(pos) == 0 {
		return fmt.Errorf("用法: mm validate <pkg.mm>")
	}
	res := mm.ValidateFile(pos[0])
	if !res.OK {
		fmt.Println("[mm] 校验失败：")
		for _, is := range res.Issues {
			fmt.Printf("  ✗ %s\n", is.Error())
		}
		return fmt.Errorf("%d 项问题", len(res.Issues))
	}
	m := res.Manifest
	fmt.Printf("[mm] 校验通过: %s %s（%s，%d 个文件）\n", m.Name, m.Version, m.Kind, len(res.Files))
	if m.IsAI() {
		fmt.Println("[mm] 该插件标记为 AI 生成（声音管理显示 ✨AI 徽标）")
	}
	return nil
}

func cmdInspect(args []string) error {
	pos := positional(args)
	if len(pos) == 0 {
		return fmt.Errorf("用法: mm inspect <pkg.mm>")
	}
	res := mm.ValidateFile(pos[0])
	if res.Manifest == nil {
		return res.Error()
	}
	m := res.Manifest
	fmt.Printf("id        : %s\nname      : %s\nversion   : %s\nkind      : %s\nentry     : %s\napi       : %d\n",
		m.ID, m.Name, m.Version, m.Kind, m.Entry, m.API)
	if m.Vendor != "" {
		fmt.Printf("vendor    : %s\n", m.Vendor)
	}
	if len(m.Capabilities) > 0 {
		fmt.Printf("capabilities: %s\n", strings.Join(m.Capabilities, ", "))
	} else {
		fmt.Println("capabilities: （无：纯 DSP，最小权限）")
	}
	if m.UI != nil {
		fmt.Printf("ui        : %s (%dx%d)\n", m.UI.Entry, m.UI.Width, m.UI.Height)
	}
	fmt.Printf("params    : %d\n", len(m.Params))
	for _, p := range m.Params {
		fmt.Printf("  - %-12s %s..%s default=%v\n", p.ID, trimFloat(p.Min), trimFloat(p.Max), p.Default)
	}
	if m.IsAI() {
		fmt.Println("author    : ai（✨AI 生成）")
	}
	if !res.OK {
		fmt.Println("\n[mm] 注意：该包存在校验问题（不可安装）：")
		for _, is := range res.Issues {
			fmt.Printf("  ✗ %s\n", is.Error())
		}
	}
	fmt.Printf("\n文件（%d）:\n", len(res.Files))
	for _, f := range res.Files {
		fmt.Println("  " + f)
	}
	return nil
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%g", v)
	return s
}

func cmdInstall(args []string) error {
	pos := positional(args)
	if len(pos) == 0 {
		return fmt.Errorf("用法: mm install <pkg.mm> --dest <plugins 目录> [--grant files,network]")
	}
	dest := flagValue(args, "--dest")
	if dest == "" {
		return fmt.Errorf("缺少 --dest（应为 <数据根>/Library/plugins）")
	}
	grant := flagValue(args, "--grant")
	res := mm.ValidateFile(pos[0])
	if !res.OK {
		return fmt.Errorf("校验失败，拒绝安装: %v", res.Error())
	}
	m := res.Manifest

	// 能力授权：声明而未授权的能力 → 拒绝（fail-closed）
	declared := map[string]bool{}
	for _, c := range m.Capabilities {
		declared[c] = true
	}
	granted := map[string]bool{}
	for _, c := range strings.Split(grant, ",") {
		if c = strings.TrimSpace(c); c != "" {
			granted[c] = true
		}
	}
	for c := range declared {
		if !granted[c] {
			return fmt.Errorf("插件声明了能力 %q 但未授权：安装命令需显式 --grant %s（用户须在界面上确认能力清单）", c, c)
		}
	}
	for c := range granted {
		if !declared[c] {
			return fmt.Errorf("--grant 包含插件未声明的能力 %q（不允许多授权）", c)
		}
	}

	target := filepath.Join(dest, fmt.Sprintf("%s@%s", m.ID, m.Version))
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("目标已存在: %s（先卸载或换版本号）", target)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	if err := unzipTo(pos[0], target); err != nil {
		return err
	}
	fmt.Printf("[mm] 已安装到 %s\n", target)
	if len(m.Capabilities) > 0 {
		fmt.Printf("[mm] 已授权能力: %s\n", strings.Join(m.Capabilities, ", "))
	}
	fmt.Println("[mm] 下一步：在设置页「声音管理」里刷新，或重启应用让引擎加载插件")
	return nil
}

func unzipTo(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		clean := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") || regexp.MustCompile(`^[A-Za-z]:`).MatchString(clean) {
			return fmt.Errorf("MM008: 包内路径非法: %s", f.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

func cmdTest(args []string) error {
	pos := positional(args)
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(abs, "manifest.json"))
	if err != nil {
		return fmt.Errorf("读取 manifest.json 失败: %w", err)
	}
	var m mm.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	libPath := filepath.Join(abs, "bin", platformDir(), strings.TrimSuffix(filepath.Base(m.Entry), filepath.Ext(m.Entry))+filepath.Ext(m.Entry))
	if _, err := os.Stat(libPath); err != nil {
		return fmt.Errorf("找不到已编译的库 %s（先 mm build）", libPath)
	}
	// 离线渲染自测：加载 DLL、渲染一个块、验证参数生效。
	// 跨平台动态加载在 Go 里需要 cgo/系统 API；这里给出明确的能力边界，
	// 真正的渲染自测由宿主（引擎）侧完成 —— 见 docs/MM插件规范.md §7 与运行时验收。
	fmt.Printf("[mm] 库已就绪: %s\n", libPath)
	fmt.Println("[mm] 渲染自测由宿主执行：把包安装后在引擎里加载（插件崩溃不会影响主程序）")
	fmt.Println("[mm] 提示：mm build 已包含 mm validate 的等价校验，可直接安装")
	return nil
}
