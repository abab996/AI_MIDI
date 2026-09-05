package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"aimidi/internal/app"
	"aimidi/internal/config"
	"aimidi/internal/engine"
	"aimidi/internal/mcp"
	"aimidi/internal/server"
)

//go:embed all:frontend
var assets embed.FS

//go:embed wails.json
var wailsCfgRaw []byte

// openBrowser 跨平台打开系统默认浏览器。此前硬编码 `cmd /c start`，
// Linux/macOS（-browser 模式的发布目标之一）永远弹不出浏览器且错误被丢弃。
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[AI_MIDI] 打开浏览器失败: %v（请手动访问 %s）", err, url)
	}
}

// loadVersion 从打包配置读取版本号注入 config（/api/version 与设置页 About
// 展示用；与 wails.json 同源，避免版本号双份维护）
func loadVersion() {
	var meta struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(wailsCfgRaw, &meta); err == nil && meta.Version != "" {
		config.AppVersion = meta.Version
	}
}

func main() {
	config.SetupLogging()
	loadVersion()

	// 启动自检：知识库目录缺失（典型为安装包漏装 Library）时打日志留痕。
	// 此前该故障全链路静默——ListLibraryFiles 返回 nil、系统提示词静默
	// 跳过文件列表，用户只能从 AI 的「文件不存在」报错反推
	if files := mcp.ListLibraryFiles(); len(files) == 0 {
		slog.Warn("Library 知识库不可用（目录缺失或为空），read_library_file 将不可用", "dir", config.LibraryDir)
	}

	browserMode := flag.Bool("browser", false, "在系统默认浏览器中打开，而不是使用原生窗口")
	scaleRatio := flag.Float64("scale", 0.8, "原生窗口占屏幕工作区的比例(0.0-1.0)，默认 0.8")
	mcpChild := flag.Bool("mcp-child", false, "内部参数：MCP 子进程模式")
	port := flag.Int("port", config.ServerPort, "HTTP 服务端口（浏览器模式）")
	flag.Parse()

	// 1. MCP 子进程模式 (不展示启动图)
	if *mcpChild {
		mcp.RunMCPServer()
		return
	}

	// 2. 单实例保护
	if !app.AcquireSingleInstanceLock("Local\\AI_MIDI_SingleInstance") {
		app.ShowErrorDialog(config.WindowTitle, "AI_MIDI 已在运行中。\n如需重新启动，请先关闭现有窗口。")
		os.Exit(0)
	}

	app.SetCurrentProcessAppID(config.WindowTitle)

	// 3. 立即在 Windows 桌面上弹出 32 位 Alpha 真透明无边框原生 Splash 窗口
	// 去掉 1.4s 假等待：改为“引擎就绪即关”，最多等 5s
	splashCtrl := app.ShowNativeTransparentSplash(5000 * time.Millisecond)

	subFS, err := fs.Sub(assets, "frontend")
	if err != nil {
		log.Fatalf("加载前端资源失败: %v", err)
	}

	appInstance := app.NewApp()
	router := server.NewRouter(subFS, appInstance.SelectFolderDialog)

	// 原生音频引擎守护（M1）：随主程序启动拉起 aimidi-engine，
	// 崩溃自动重启；进程退出时优雅回收。缺失/失败均不阻断主程序（降级 Web Audio）。
	audioSettings := config.LoadSettings().Audio
	engineSup := engine.NewSupervisor(engine.Config{
		SoundFontDir: filepath.Join(config.LibraryDir, "soundfonts"),
		// 路径白名单目录（engine 不反向依赖 config，见 engine.Config.Dirs 注释）
		Dirs: func() (string, string, []string) {
			return filepath.Join(config.LibraryDir, "soundfonts"),
				config.OutputDir,
				config.LoadSettings().MaterialDirs
		},
	}, audioSettings)
	engine.SetGlobal(engineSup)
	engineSup.Start()
	defer engineSup.Stop()

	// 启动图跟随引擎：就绪/失败/禁用即关（去掉固定 1s 假等待），最多 5s 兜底
	go func() {
		if !audioSettings.EngineEnabled {
			time.Sleep(300 * time.Millisecond)
			splashCtrl.Close()
			return
		}
		deadline := time.Now().Add(5000 * time.Millisecond)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			st := engineSup.Status()
			if st.State == engine.StateReady || st.State == engine.StateFailed || st.State == engine.StateDisabled {
				time.Sleep(150 * time.Millisecond)
				splashCtrl.Close()
				return
			}
			if time.Now().After(deadline) {
				splashCtrl.Close()
				return
			}
			<-ticker.C
		}
	}()

	// 4. 浏览器模式
	if *browserMode {
		addr := fmt.Sprintf("127.0.0.1:%d", *port)
		go func() {
			splashCtrl.Wait()
			openBrowser(fmt.Sprintf("http://%s/chat.html", addr))
		}()

		fmt.Printf("[AI_MIDI] HTTP 服务已在 http://%s 启动 (浏览器模式)\n", addr)
		// 不设 ReadTimeout/WriteTimeout：WriteTimeout 会掐断 SSE 长下行。
		// 慢速请求体的保护由 ReadHeaderTimeout + handler 侧 LimitReader/MaxBytes 承担
		srv := &http.Server{
			Addr:              addr,
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil {
			app.ShowErrorDialog(config.WindowTitle, fmt.Sprintf("服务启动失败: %v", err))
		}
		return
	}

	// 5. Wails 原生窗口模式 (预先隐藏，等待桌面透明启动图结束并居中弹出)
	workW, workH, dpiScale := app.GetLogicalWorkArea()
	ratio := *scaleRatio
	if ratio < 0.1 {
		ratio = 0.1
	}
	if ratio > 1.0 {
		ratio = 1.0
	}

	width := int(float64(workW) * ratio)
	height := int(float64(workH) * ratio)
	minW := int(float64(width) * 0.8)
	minH := int(float64(height) * 0.8)

	fmt.Printf("[AI_MIDI] DPI scale=%.2f, work area=%dx%d (logical), window=%dx%d (logical), ratio=%.2f\n",
		dpiScale, workW, workH, width, height, ratio)

	err = wails.Run(&options.App{
		Title:             config.WindowTitle,
		Width:             width,
		Height:            height,
		MinWidth:          minW,
		MinHeight:         minH,
		StartHidden:       true, // 先隐藏，等桌面透明 Splash 播放完成再展示
		HideWindowOnClose: false,
		OnStartup: func(ctx context.Context) {
			appInstance.Startup(ctx)
			go func() {
				// 等待原生桌面透明 Splash 完成（DOM 就绪后会提前关闭）
				splashCtrl.Wait()
				runtime.WindowCenter(ctx)
				runtime.WindowShow(ctx)
			}()
		},
		// 前端 DOM 就绪后不再假等待 400ms，启动图已由引擎就绪驱动关闭
		OnDomReady: func(ctx context.Context) {
		},
		Bind: []interface{}{
			appInstance,
		},
		AssetServer: &assetserver.Options{
			Assets:  subFS,
			Handler: router,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			BackdropType:         windows.None,
			Theme:                windows.SystemDefault,
		},
	})

	if err != nil {
		app.ShowErrorDialog(config.WindowTitle, fmt.Sprintf("应用程序运行异常: %v", err))
	}
}
