package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
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

func main() {
	config.SetupLogging()

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

	// 3. 立即在 Windows 桌面上弹出 32 位 Alpha 真透明无边框原生 Splash 窗口（至少展示 1.4 秒）
	splashCtrl := app.ShowNativeTransparentSplash(1400 * time.Millisecond)

	subFS, err := fs.Sub(assets, "frontend")
	if err != nil {
		log.Fatalf("加载前端资源失败: %v", err)
	}

	appInstance := app.NewApp()
	router := server.NewRouter(subFS, appInstance.SelectFolderDialog)

	// 原生音频引擎守护（M1）：随主程序启动拉起 aimidi-engine，
	// 崩溃自动重启；进程退出时优雅回收。缺失/失败均不阻断主程序（降级 Web Audio）。
	audioSettings := config.LoadSettings().Audio
	engineSup := engine.NewSupervisor(engine.Config{}, audioSettings)
	engine.SetGlobal(engineSup)
	engineSup.Start()
	defer engineSup.Stop()

	// 4. 浏览器模式
	if *browserMode {
		addr := fmt.Sprintf("127.0.0.1:%d", *port)
		go func() {
			splashCtrl.Wait()
			_ = exec.Command("cmd", "/c", "start", fmt.Sprintf("http://%s/chat.html", addr)).Start()
		}()

		fmt.Printf("[AI_MIDI] HTTP 服务已在 http://%s 启动 (浏览器模式)\n", addr)
		if err := http.ListenAndServe(addr, router); err != nil {
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
		// 前端 DOM 就绪（首帧已具备渲染条件）后提前关闭启动图——
		// 1400ms 固定时长改为上限，实际展示 = 图片解码 + DOM 就绪 + 短暂留白
		OnDomReady: func(ctx context.Context) {
			go func() {
				time.Sleep(400 * time.Millisecond)
				splashCtrl.Close()
			}()
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
