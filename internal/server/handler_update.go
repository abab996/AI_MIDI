package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"aimidi/internal/config"
	"aimidi/internal/update"
)

// updater 更新检查与下载的进程内状态（挂在 Router 上，随应用生命周期存在）
type updater struct {
	client     *update.Client
	downloader *update.Downloader
}

func newUpdater() *updater {
	u := &updater{client: update.NewClient(config.UpdateManifestURL)}
	// 仅 Windows 走「应用内下载 + 运行安装包」；下载成功后启动安装程序，
	// 安装脚本（AI_MIDI.iss）会自动结束旧进程并原位升级、保留用户数据
	if runtime.GOOS == "windows" {
		u.downloader = update.NewDownloader(launchInstaller)
	}
	return u
}

// launchInstaller 运行下载好的安装包（detached：本进程随后被安装脚本结束，
// 安装程序必须独立于本进程存活）。Inno /SILENT：跳过向导页、仅显示安装进度；
// /NORESTART：不强制重启系统。
func launchInstaller(path string) error {
	return exec.Command(path, "/SILENT", "/NORESTART").Start()
}

// MandatoryPending 强制更新判定：缓存清单标记 mandatory 且版本高于当前。
// 仅用已缓存清单（从不阻塞请求）；清单从未成功拉取时返回 false（fail-open）。
func (u *updater) MandatoryPending() bool {
	m := u.client.Cached()
	if m == nil || !m.Mandatory {
		return false
	}
	return update.CompareVersions(m.Version, config.AppVersion) > 0
}

// PrefetchUpdate 启动时后台预取清单，让首次 /api/update/check 即时返回
func (r *Router) PrefetchUpdate() {
	_, _ = r.updater.client.Fetch(context.Background())
}

// handleUpdateCheck GET /api/update/check：比较当前版本与清单版本
func (r *Router) handleUpdateCheck(w http.ResponseWriter, req *http.Request) {
	mani, err := r.updater.client.Fetch(req.Context())
	if err != nil {
		// fail-open：拉取失败按「无更新」处理，前端保持静默
		writeJSON(w, http.StatusOK, map[string]any{
			"available":    false,
			"check_failed": true,
			"current":      config.AppVersion,
		})
		return
	}

	available := update.CompareVersions(mani.Version, config.AppVersion) > 0
	writeJSON(w, http.StatusOK, map[string]any{
		"available":    available,
		"mandatory":    available && mani.Mandatory,
		"current":      config.AppVersion,
		"latest":       mani.Version,
		"date":         mani.Date,
		"notes":        mani.Notes,
		"download_url": mani.DownloadURLFor(runtime.GOOS),
	})
}

// handleUpdateApply POST /api/update/apply：立即更新。
// Windows：应用内下载安装包（进度走 /api/update/progress），完成后自动运行；
// 其他平台：调起系统浏览器直接下载。
func (r *Router) handleUpdateApply(w http.ResponseWriter, req *http.Request) {
	mani, err := r.updater.client.Fetch(req.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法获取更新信息，请稍后重试")
		return
	}
	if update.CompareVersions(mani.Version, config.AppVersion) <= 0 {
		writeError(w, http.StatusBadRequest, "当前已是最新版本")
		return
	}

	dlURL := mani.DownloadURLFor(runtime.GOOS)
	if dlURL == "" {
		writeError(w, http.StatusNotFound, "清单中没有当前平台的下载链接")
		return
	}

	if r.updater.downloader == nil {
		// 非 Windows：浏览器直接下载
		openExternal(dlURL)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "browser"})
		return
	}

	// Windows：单飞下载（重复点击直接返回当前进度）。
	// 版本号来自远程清单，必须消毒后才能拼路径（防 ..\ 路径穿越）
	if !update.IsValidVersionString(mani.Version) {
		writeError(w, http.StatusBadGateway, "更新清单版本号非法: "+mani.Version)
		return
	}
	dest := filepath.Join(updateDownloadDir(), fmt.Sprintf("AI_MIDI_Setup_%s_windows_amd64.exe", mani.Version))
	// 注意：下载生命周期独立于本次 HTTP 请求——req.Context() 在 handler
	// 返回后即被取消，传入会让下载瞬间中断（这是此前的致命 bug）
	if err := r.updater.downloader.Start(context.Background(), dlURL, dest, mani.SHA256Windows); err != nil && err != update.ErrInProgress {
		writeError(w, http.StatusInternalServerError, "启动下载失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "download", "progress": r.updater.downloader.Snapshot()})
}

// updateDownloadDir 安装包下载目录。不用 %TEMP%：系统清理器可能几秒内
// 删掉临时目录下的文件（发布指南 §10 记录过该坑），下载完成但启动失败时
// 用户会指向一个已消失的文件。用户缓存目录稳定且无需管理员权限。
// var 形式便于测试注入临时目录。
var updateDownloadDir = func() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "AI_MIDI")
	}
	return os.TempDir()
}

// handleUpdateProgress GET /api/update/progress：下载进度（前端轮询）
func (r *Router) handleUpdateProgress(w http.ResponseWriter, req *http.Request) {
	if r.updater.downloader == nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": "idle"})
		return
	}
	writeJSON(w, http.StatusOK, r.updater.downloader.Snapshot())
}

// handleUpdateOpenBrowser POST /api/update/open-browser：下载失败时的降级通道，
// 调起系统浏览器下载。URL 只取自清单，不接受客户端传参（防滥用开放跳转）。
func (r *Router) handleUpdateOpenBrowser(w http.ResponseWriter, req *http.Request) {
	mani, err := r.updater.client.Fetch(req.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法获取更新信息，请稍后重试")
		return
	}
	dlURL := mani.DownloadURLFor(runtime.GOOS)
	if dlURL == "" {
		writeError(w, http.StatusNotFound, "清单中没有当前平台的下载链接")
		return
	}
	if err := openExternal(dlURL); err != nil {
		writeError(w, http.StatusInternalServerError, "打开浏览器失败，请手动访问："+dlURL)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// updateGateWhitelist 强制更新期间仍然放行的非 GET 路径前缀
// （GET 一律放行：查询/静态资源不拦，避免把界面本身锁死）
func updateGateWhitelisted(path string) bool {
	switch path {
	case "/api/health", "/api/version", "/api/client-error":
		return true
	}
	return len(path) >= len("/api/update/") && path[:len("/api/update/")] == "/api/update/"
}

// openExternal 调起系统默认浏览器（与 main.go openBrowser 同逻辑；
// 独立实现避免 server 反向依赖 main 包）
func openExternal(url string) error {
	return externalOpener(url)
}

var externalOpener = defaultExternalOpener

func defaultExternalOpener(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// "start" 会把带引号首参当窗口标题，标题占位 "_"
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
