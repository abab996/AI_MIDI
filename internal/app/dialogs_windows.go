package app

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"aimidi/internal/config"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	procCreateMutexW          = kernel32.NewProc("CreateMutexW")
	procGetLastError          = kernel32.NewProc("GetLastError")
	procMessageBoxW           = user32.NewProc("MessageBoxW")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")
	procGetDeviceCaps         = gdi32.NewProc("GetDeviceCaps") // GetDeviceCaps 是 GDI 函数（原误挂 user32，桌面模式启动即 panic）
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procSetProcessDPIAware    = user32.NewProc("SetProcessDPIAware")
	procSetAppUserModelID     = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procLoadImageW            = user32.NewProc("LoadImageW")
	procSendMessageW          = user32.NewProc("SendMessageW")
	procSHBrowseForFolderW    = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW  = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitializeEx        = ole32.NewProc("CoInitializeEx")
	procCoUninitialize        = ole32.NewProc("CoUninitialize")
	procCoTaskMemFree         = ole32.NewProc("CoTaskMemFree")
)

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

var singleInstanceHandle uintptr

// AcquireSingleInstanceLock 获取 Windows 全局单实例互斥锁
func AcquireSingleInstanceLock(name string) bool {
	namePtr, _ := syscall.UTF16PtrFromString(name)
	h, _, _ := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	singleInstanceHandle = h
	lastErr, _, _ := procGetLastError.Call()
	return lastErr != 183 // ERROR_ALREADY_EXISTS = 183
}

// SelectFolderDialog 打开原生文件夹选择对话框（Windows）。
// 桌面模式走 Wails 对话框（自动挂主窗口）；浏览器模式（-browser）
// 没有 Wails 运行时，直接调 Win32 弹窗——两种模式后端都能弹。
func (a *App) SelectFolderDialog() (string, error) {
	a.ctxMu.RLock()
	ctx := a.ctx
	a.ctxMu.RUnlock()

	if ctx != nil {
		return wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
			Title: "选择工作区目录",
		})
	}
	return SelectFolderNative("选择工作区目录")
}

// ShowErrorDialog 弹出 Windows 原生错误消息框
func ShowErrorDialog(title, message string) {
	tPtr, _ := syscall.UTF16PtrFromString(title)
	mPtr, _ := syscall.UTF16PtrFromString(message)
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(mPtr)), uintptr(unsafe.Pointer(tPtr)), 0x10) // MB_ICONERROR
}

// browseInfoW Win32 BROWSEINFOW（目录选择对话框参数）
type browseInfoW struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName uintptr
	lpszTitle      uintptr
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

// SelectFolderNative 纯 Win32 目录选择对话框（SHBrowseForFolder）。
// 不依赖 Wails 运行时——浏览器模式（-browser，无 Wails ctx）下
// 后端仍可直接弹出系统目录选择器；返回空串表示用户取消。
func SelectFolderNative(title string) (string, error) {
	// COM 对话框要求单线程套间；锁住 OS 线程避免 goroutine 迁移导致
	// CoInitializeEx 与弹窗落在不同线程
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	const coinitAPARTMENTTHREADED = 0x2
	hr, _, _ := procCoInitializeEx.Call(0, coinitAPARTMENTTHREADED)
	if hr == 0 || hr == 1 { // S_OK / S_FALSE：本次调用负责反初始化
		defer procCoUninitialize.Call()
	}

	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return "", err
	}
	display := make([]uint16, 260)

	const (
		bifReturnOnlyFSDirs = 0x0001
		bifEditBox          = 0x0010
		bifNewDialogStyle   = 0x0040
	)
	bi := browseInfoW{
		lpszTitle:      uintptr(unsafe.Pointer(titlePtr)),
		pszDisplayName: uintptr(unsafe.Pointer(&display[0])),
		ulFlags:        bifReturnOnlyFSDirs | bifEditBox | bifNewDialogStyle,
	}

	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "", nil // 用户取消
	}
	defer procCoTaskMemFree.Call(pidl)

	pathBuf := make([]uint16, 32768)
	ret, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&pathBuf[0])))
	if ret == 0 {
		return "", nil
	}
	return syscall.UTF16ToString(pathBuf), nil
}

// SetCurrentProcessAppID 为当前进程设置显式 AppUserModelID
func SetCurrentProcessAppID(appID string) {
	ptr, _ := syscall.UTF16PtrFromString(appID)
	_, _, _ = procSetAppUserModelID.Call(uintptr(unsafe.Pointer(ptr)))
}

// SetNativeWindowIcon 动态切换 Windows 原生窗口与任务栏图标 (冷暖色调实时同步)
func SetNativeWindowIcon(windowTitle string, iconFilename string) {
	tPtr, _ := syscall.UTF16PtrFromString(windowTitle)
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(tPtr)))
	if hwnd == 0 {
		return
	}

	iconPath := filepath.Join(config.ProjectRoot, iconFilename)
	if _, err := os.Stat(iconPath); err != nil {
		return
	}

	pathPtr, _ := syscall.UTF16PtrFromString(iconPath)
	// IMAGE_ICON = 1, LR_LOADFROMFILE = 0x0010, LR_DEFAULTSIZE = 0x0040
	hBig, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathPtr)), 1, 32, 32, 0x0010|0x0040)
	hSmall, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathPtr)), 1, 16, 16, 0x0010|0x0040)

	// WM_SETICON = 0x0080, ICON_BIG = 1, ICON_SMALL = 0
	if hBig != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, 0x0080, 1, hBig)
	}
	if hSmall != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, 0x0080, 0, hSmall)
	}
}

// GetLogicalWorkArea 获取主显示器工作区尺寸与 DPI 缩放比例
func GetLogicalWorkArea() (int, int, float64) {
	_, _, _ = procSetProcessDPIAware.Call()

	dpiScale := 1.0
	hdc, _, _ := procGetDC.Call(0)
	if hdc != 0 {
		dpi, _, _ := procGetDeviceCaps.Call(hdc, 88) // LOGPIXELSX
		_, _, _ = procReleaseDC.Call(0, hdc)
		if dpi > 0 {
			dpiScale = float64(dpi) / 96.0
			if dpiScale < 1.0 {
				dpiScale = 1.0
			}
		}
	}

	var r rect
	ret, _, _ := procSystemParametersInfoW.Call(0x0030, 0, uintptr(unsafe.Pointer(&r)), 0) // SPI_GETWORKAREA
	workW := 1920
	workH := 1080
	if ret != 0 {
		workW = int(r.Right - r.Left)
		workH = int(r.Bottom - r.Top)
	}

	logicalW := int(float64(workW) / dpiScale)
	logicalH := int(float64(workH) / dpiScale)

	if logicalW <= 0 {
		logicalW = 1280
	}
	if logicalH <= 0 {
		logicalH = 800
	}

	return logicalW, logicalH, dpiScale
}
