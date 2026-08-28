//go:build !windows

package app

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 非 Windows 平台实现：桌面模式（Wails ctx 可用）走 Wails 对话框；
// 浏览器模式优先尝试 zenity（主流发行版常见），缺失时返回空串、
// 前端回退手输路径。图标/消息框等原生窗口能力以日志降级。

var instanceLockFile *os.File

// AcquireSingleInstanceLock 单实例保护：对临时目录锁文件加 flock。
// 进程退出时内核自动释放锁，无陈旧锁问题。
func AcquireSingleInstanceLock(name string) bool {
	lockPath := filepath.Join(os.TempDir(), "AI_MIDI_"+strings.ReplaceAll(strings.TrimPrefix(name, "Local\\"), "\\", "_")+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		slog.Warn("[app] 单实例锁文件创建失败（放行继续）", "path", lockPath, "err", err)
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		slog.Warn("[app] 检测到已有 AI_MIDI 实例在运行", "lock", lockPath)
		return false
	}
	instanceLockFile = f // 持有句柄直至进程退出，锁随之释放
	return true
}

// ShowErrorDialog 非 Windows 无统一原生消息框：记日志并输出 stderr。
func ShowErrorDialog(title, message string) {
	slog.Error("[app] " + title + ": " + message)
}

// SetCurrentProcessAppID Linux 无 AppUserModelID 概念，空实现。
func SetCurrentProcessAppID(appID string) {}

// SetNativeWindowIcon 窗口图标由 Wails/桌面环境接管，空实现。
func SetNativeWindowIcon(windowTitle string, iconFilename string) {}

// GetLogicalWorkArea 非 Windows 交由 Wails/GTK 自行处理 DPI，
// 返回保守默认值供窗口尺寸计算。
func GetLogicalWorkArea() (int, int, float64) {
	return 1280, 800, 1.0
}

// SelectFolderDialog 打开文件夹选择对话框（非 Windows）。
// 桌面模式走 Wails 对话框（经 GTK/portal）；浏览器模式无 Wails
// 运行时，回退 zenity（主流发行版常见）；都没有则返回空串、前端回退手输。
func (a *App) SelectFolderDialog() (string, error) {
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()

	if ctx != nil {
		return runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{
			Title: "选择工作区目录",
		})
	}

	const dialogTitle = "选择工作区目录"
	if _, err := exec.LookPath("zenity"); err == nil {
		out, err := exec.Command("zenity", "--file-selection", "--directory",
			"--title", dialogTitle).Output()
		if err != nil {
			return "", nil // 用户取消（zenity 以非零退出码表示取消）
		}
		return strings.TrimSpace(string(out)), nil
	}
	slog.Warn("[app] 非 Windows 浏览器模式弹出目录选择器需要 zenity，未安装则请手动输入路径")
	return "", nil
}
