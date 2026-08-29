package app

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"aimidi/internal/config"
)

var (
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")
	procShowWindow          = user32.NewProc("ShowWindow")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
)

type point struct {
	X int32
	Y int32
}

type sizeStruct struct {
	CX int32
	CY int32
}

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
	BmiColors [1]uint32
}

type winMsg struct {
	Hwnd     uintptr
	Message  uint32
	_pad     uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

// SplashController 用于控制启动图窗口的生命周期
type SplashController struct {
	hwnd      uintptr
	done      chan struct{}
	closeChan chan struct{}
	closeOnce sync.Once
}

// ShowNativeTransparentSplash 显示类似 Photoshop / 原版 Python 的原生桌面级 32 位 Alpha 真透明无边框 Splash 窗口
func ShowNativeTransparentSplash(duration time.Duration) *SplashController {
	ctrl := &SplashController{
		done:      make(chan struct{}),
		closeChan: make(chan struct{}),
	}

	go func() {
		// 关键修复 1：将当前 Goroutine 与当前操作系统线程 (OS Thread) 强绑定。
		// Win32 API 规定窗口句柄 (HWND) 严格归属于创建它的操作系统线程，且 DestroyWindow 必须由创建窗口的同一个 OS 线程调用。
		// 若未调用 LockOSThread，Go 调度器在 time.Sleep 后可能将 Goroutine 调度至其他 OS 线程，导致 DestroyWindow 跨线程调用直接被系统拒绝（返回 ERROR_ACCESS_DENIED），启动图因而滞留桌面无法消失。
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(ctrl.done)

		// 1. 读取当前主题对应的启动图
		splashFile := "splash_dark.png"
		themePath := filepath.Join(config.ProjectRoot, "theme.txt")
		if data, err := os.ReadFile(themePath); err == nil && string(bytes.TrimSpace(data)) == "parchment" {
			splashFile = "splash_warm.png"
		}

		splashPath := filepath.Join(config.ProjectRoot, splashFile)
		f, err := os.Open(splashPath)
		if err != nil {
			// 回退尝试常规 splash.png
			splashPath = filepath.Join(config.ProjectRoot, "splash.png")
			f, err = os.Open(splashPath)
			if err != nil {
				return
			}
		}
		defer f.Close()

		img, err := png.Decode(f)
		if err != nil {
			return
		}

		bounds := img.Bounds()
		srcW := int32(bounds.Dx())
		srcH := int32(bounds.Dy())
		if srcW <= 0 || srcH <= 0 {
			return
		}

		// 2. 根据系统 DPI 动态计算启动窗口尺寸 (基准逻辑画幅 780x420)
		workW, workH, dpiScale := GetLogicalWorkArea()
		if dpiScale < 1.0 {
			dpiScale = 1.0
		}
		imgW := int32(float64(780) * dpiScale)
		imgH := int32(float64(420) * dpiScale)

		// 3. 准备 32 位 Top-Down DIB 位图内存 (BGRA 预乘 Alpha)
		hdcScreen, _, _ := procGetDC.Call(0)
		if hdcScreen == 0 {
			return
		}
		defer procReleaseDC.Call(0, hdcScreen)

		hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
		if hdcMem == 0 {
			return
		}
		defer procDeleteDC.Call(hdcMem)

		var bmi bitmapInfo
		bmi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bmi.BmiHeader))
		bmi.BmiHeader.BiWidth = imgW
		bmi.BmiHeader.BiHeight = -imgH // Top-down
		bmi.BmiHeader.BiPlanes = 1
		bmi.BmiHeader.BiBitCount = 32
		bmi.BmiHeader.BiCompression = 0 // BI_RGB

		var pBits unsafe.Pointer
		hDIB, _, _ := procCreateDIBSection.Call(
			hdcMem,
			uintptr(unsafe.Pointer(&bmi)),
			0, // DIB_RGB_COLORS
			uintptr(unsafe.Pointer(&pBits)),
			0,
			0,
		)
		if hDIB == 0 || pBits == nil {
			return
		}
		defer procDeleteObject.Call(hDIB)

		oldObj, _, _ := procSelectObject.Call(hdcMem, hDIB)
		defer procSelectObject.Call(hdcMem, oldObj)

		// 从母图中映射到 DPI 缩放后尺寸。
		// 优先直接访问解码位图的 Pix 数组（PNG 解码通常得到 NRGBA/RGBA），
		// 避免约 74 万次 img.At() 接口调用（1.5×DPI 下会拖慢启动图出现）
		getPix := func(x, y int) (byte, byte, byte, byte) {
			r, g, b, a := img.At(x, y).RGBA()
			return byte(r >> 8), byte(g >> 8), byte(b >> 8), byte(a >> 8)
		}
		switch im := img.(type) {
		case *image.NRGBA:
			// NRGBA 为非预乘格式，需按 alpha 预乘后供 UpdateLayeredWindow 使用
			getPix = func(x, y int) (byte, byte, byte, byte) {
				i := im.PixOffset(x, y)
				r, g, b, a := im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3]
				if a == 255 {
					return r, g, b, a
				}
				return byte(uint16(r) * uint16(a) / 255),
					byte(uint16(g) * uint16(a) / 255),
					byte(uint16(b) * uint16(a) / 255),
					a
			}
		case *image.RGBA:
			// RGBA 本身就是预乘格式，直接取值
			getPix = func(x, y int) (byte, byte, byte, byte) {
				i := im.PixOffset(x, y)
				return im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3]
			}
		}

		pixels := unsafe.Slice((*byte)(pBits), imgW*imgH*4)
		for y := 0; y < int(imgH); y++ {
			srcY := bounds.Min.Y + int(float64(y)*float64(srcH)/float64(imgH))
			if srcY >= bounds.Max.Y {
				srcY = bounds.Max.Y - 1
			}
			for x := 0; x < int(imgW); x++ {
				srcX := bounds.Min.X + int(float64(x)*float64(srcW)/float64(imgW))
				if srcX >= bounds.Max.X {
					srcX = bounds.Max.X - 1
				}
				r, g, b, a := getPix(srcX, srcY)
				idx := (y*int(imgW) + x) * 4
				// 直接按字节写入（源已是预乘或不含半透明的大面积区域）
				pixels[idx+0] = b
				pixels[idx+1] = g
				pixels[idx+2] = r
				pixels[idx+3] = a
			}
		}

		// 4. 计算高 DPI 下的屏幕居中物理坐标
		screenW := int32(float64(workW) * dpiScale)
		screenH := int32(float64(workH) * dpiScale)
		posX := (screenW - imgW) / 2
		posY := (screenH - imgH) / 2

		// 5. 创建 Windows 原生分层窗口 (WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TOOLWINDOW)
		className, _ := syscall.UTF16PtrFromString("STATIC")
		windowTitle, _ := syscall.UTF16PtrFromString("AI_MIDI_Splash")
		hwnd, _, _ := procCreateWindowExW.Call(
			0x00080000|0x00000008|0x00000080, // WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TOOLWINDOW
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(windowTitle)),
			0x80000000, // WS_POPUP
			uintptr(posX),
			uintptr(posY),
			uintptr(imgW),
			uintptr(imgH),
			0,
			0,
			0,
			0,
		)
		if hwnd == 0 {
			return
		}
		ctrl.hwnd = hwnd

		// 关键修复 2：由创建该 HWND 的同一操作系统线程确保在退出前 Hide 并 Destroy 窗口
		defer func() {
			if ctrl.hwnd != 0 {
				procShowWindow.Call(ctrl.hwnd, 0) // SW_HIDE
				procDestroyWindow.Call(ctrl.hwnd)
				ctrl.hwnd = 0
			}
		}()

		// 6. 应用 32 位 Alpha 透明度通道
		ptDst := point{X: posX, Y: posY}
		sz := sizeStruct{CX: imgW, CY: imgH}
		ptSrc := point{X: 0, Y: 0}
		blend := blendFunction{
			BlendOp:             0, // AC_SRC_OVER
			BlendFlags:          0,
			SourceConstantAlpha: 255,
			AlphaFormat:         1, // AC_SRC_ALPHA
		}

		procUpdateLayeredWindow.Call(
			hwnd,
			hdcScreen,
			uintptr(unsafe.Pointer(&ptDst)),
			uintptr(unsafe.Pointer(&sz)),
			hdcMem,
			uintptr(unsafe.Pointer(&ptSrc)),
			0,
			uintptr(unsafe.Pointer(&blend)),
			2, // ULW_ALPHA
		)

		procShowWindow.Call(hwnd, 5) // SW_SHOW

		// 关键修复 3：在绑定的 OS 线程中处理 Windows 消息泵与倒计时/提前退出
		deadline := time.Now().Add(duration)
		var msg winMsg
		for {
			now := time.Now()
			if now.After(deadline) {
				break
			}
			remaining := deadline.Sub(now)

			// 检查是否被外部主动调用了 Close()
			select {
			case <-ctrl.closeChan:
				return
			default:
			}

			// 处理该线程的所有 Windows 消息，防止系统判定无响应
			for {
				ret, _, _ := procPeekMessageW.Call(
					uintptr(unsafe.Pointer(&msg)),
					0,
					0,
					0,
					1, // PM_REMOVE
				)
				if ret == 0 {
					break
				}
				if msg.Message == 0x0012 { // WM_QUIT
					return
				}
				procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
				procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
			}

			sleepStep := 15 * time.Millisecond
			if remaining < sleepStep {
				sleepStep = remaining
			}
			time.Sleep(sleepStep)
		}
	}()

	return ctrl
}

// Close 手动提前关闭 Splash 窗口 (并发安全，通过 Channel 通知绑定的 OS 线程执行销毁)
func (c *SplashController) Close() {
	if c != nil {
		c.closeOnce.Do(func() {
			close(c.closeChan)
		})
	}
}

// Wait 等待 Splash 窗口自然结束
func (c *SplashController) Wait() {
	if c != nil && c.done != nil {
		<-c.done
	}
}
