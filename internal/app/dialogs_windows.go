package app

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

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
	procGetDeviceCaps         = gdi32.NewProc("GetDeviceCaps")
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procSetProcessDPIAware    = user32.NewProc("SetProcessDPIAware")
	procSetAppUserModelID     = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procLoadImageW            = user32.NewProc("LoadImageW")
	procSendMessageW          = user32.NewProc("SendMessageW")
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

// ShowErrorDialog 弹出 Windows 原生错误消息框
func ShowErrorDialog(title, message string) {
	tPtr, _ := syscall.UTF16PtrFromString(title)
	mPtr, _ := syscall.UTF16PtrFromString(message)
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(mPtr)), uintptr(unsafe.Pointer(tPtr)), 0x10) // MB_ICONERROR
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
