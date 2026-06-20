"""AI_MIDI 独立启动器。

显示无边框启动图,后台启动 Gradio 服务,就绪后再打开原生窗口。
"""

from __future__ import annotations

import argparse
import ctypes
import ctypes.wintypes
import sys
import threading
import time
import tkinter as tk
import urllib.request
import webbrowser
from dataclasses import dataclass
from pathlib import Path
from tkinter import messagebox

# Windows 打包后标准输出可能使用 GBK,提前设为 UTF-8 避免 emoji/中文打印崩溃。
if sys.platform == "win32":
    for _stream in (sys.stdout, sys.stderr):
        if _stream is not None and hasattr(_stream, "reconfigure"):
            _stream.reconfigure(encoding="utf-8")

from PIL import Image
import webview

import config
import webui


# 启动图单独使用 Go.png,不影响 pywebview 窗口图标(仍用 webui.SPLASH_IMAGE / app_icon.ico)
SPLASH_IMAGE = Path(__file__).with_name("Go.png")


GWL_EXSTYLE = -20
WS_EX_TOOLWINDOW = 0x00000080
WS_EX_LAYERED = 0x00080000
WS_EX_APPWINDOW = 0x00040000
SWP_NOMOVE = 0x0002
SWP_NOSIZE = 0x0001
SWP_NOZORDER = 0x0004
SWP_FRAMECHANGED = 0x0020

ULW_ALPHA = 0x00000002
AC_SRC_OVER = 0x00
AC_SRC_ALPHA = 0x01
DIB_RGB_COLORS = 0


class _BITMAPINFOHEADER(ctypes.Structure):
    _fields_ = [
        ("biSize", ctypes.wintypes.DWORD),
        ("biWidth", ctypes.wintypes.LONG),
        ("biHeight", ctypes.wintypes.LONG),
        ("biPlanes", ctypes.wintypes.WORD),
        ("biBitCount", ctypes.wintypes.WORD),
        ("biCompression", ctypes.wintypes.DWORD),
        ("biSizeImage", ctypes.wintypes.DWORD),
        ("biXPelsPerMeter", ctypes.wintypes.LONG),
        ("biYPelsPerMeter", ctypes.wintypes.LONG),
        ("biClrUsed", ctypes.wintypes.DWORD),
        ("biClrImportant", ctypes.wintypes.DWORD),
    ]


class _BITMAPINFO(ctypes.Structure):
    _fields_ = [("bmiHeader", _BITMAPINFOHEADER)]


class _POINT(ctypes.Structure):
    _fields_ = [("x", ctypes.wintypes.LONG), ("y", ctypes.wintypes.LONG)]


class _BLENDFUNCTION(ctypes.Structure):
    _fields_ = [
        ("BlendOp", ctypes.wintypes.BYTE),
        ("BlendFlags", ctypes.wintypes.BYTE),
        ("SourceConstantAlpha", ctypes.wintypes.BYTE),
        ("AlphaFormat", ctypes.wintypes.BYTE),
    ]


_user32 = ctypes.windll.user32
_gdi32 = ctypes.windll.gdi32

GetWindowLongW = _user32.GetWindowLongW
GetWindowLongW.restype = ctypes.wintypes.LONG
SetWindowLongW = _user32.SetWindowLongW
SetWindowLongW.argtypes = [ctypes.wintypes.HWND, ctypes.wintypes.INT, ctypes.wintypes.LONG]
SetWindowLongW.restype = ctypes.wintypes.LONG
SetWindowPos = _user32.SetWindowPos
SetWindowPos.argtypes = [
    ctypes.wintypes.HWND,
    ctypes.wintypes.HWND,
    ctypes.wintypes.INT,
    ctypes.wintypes.INT,
    ctypes.wintypes.INT,
    ctypes.wintypes.INT,
    ctypes.wintypes.UINT,
]
SetWindowPos.restype = ctypes.wintypes.BOOL

UpdateLayeredWindow = _user32.UpdateLayeredWindow
UpdateLayeredWindow.argtypes = [
    ctypes.wintypes.HWND,
    ctypes.wintypes.HDC,
    ctypes.POINTER(_POINT),
    ctypes.POINTER(_POINT),
    ctypes.wintypes.HDC,
    ctypes.POINTER(_POINT),
    ctypes.wintypes.COLORREF,
    ctypes.POINTER(_BLENDFUNCTION),
    ctypes.wintypes.DWORD,
]
UpdateLayeredWindow.restype = ctypes.wintypes.BOOL

GetDC = _user32.GetDC
GetDC.argtypes = [ctypes.wintypes.HWND]
GetDC.restype = ctypes.wintypes.HDC
ReleaseDC = _user32.ReleaseDC
ReleaseDC.argtypes = [ctypes.wintypes.HWND, ctypes.wintypes.HDC]
ReleaseDC.restype = ctypes.wintypes.INT

CreateCompatibleDC = _gdi32.CreateCompatibleDC
CreateCompatibleDC.argtypes = [ctypes.wintypes.HDC]
CreateCompatibleDC.restype = ctypes.wintypes.HDC
DeleteDC = _gdi32.DeleteDC
DeleteDC.argtypes = [ctypes.wintypes.HDC]
DeleteDC.restype = ctypes.wintypes.BOOL

CreateDIBSection = _gdi32.CreateDIBSection
CreateDIBSection.argtypes = [
    ctypes.wintypes.HDC,
    ctypes.POINTER(_BITMAPINFO),
    ctypes.wintypes.UINT,
    ctypes.POINTER(ctypes.c_void_p),
    ctypes.wintypes.HANDLE,
    ctypes.wintypes.DWORD,
]
CreateDIBSection.restype = ctypes.wintypes.HBITMAP

SelectObject = _gdi32.SelectObject
SelectObject.argtypes = [ctypes.wintypes.HDC, ctypes.wintypes.HGDIOBJ]
SelectObject.restype = ctypes.wintypes.HGDIOBJ
DeleteObject = _gdi32.DeleteObject
DeleteObject.argtypes = [ctypes.wintypes.HGDIOBJ]
DeleteObject.restype = ctypes.wintypes.BOOL
_memmove = ctypes.memmove
_memmove.argtypes = [ctypes.c_void_p, ctypes.c_void_p, ctypes.c_size_t]
_memmove.restype = ctypes.c_void_p


@dataclass
class LaunchState:
    """后台启动状态。"""

    ready: bool = False
    failed: bool = False
    local_url: str = ""
    error: str = ""


def _get_work_area() -> tuple[int, int]:
    """获取主显示器工作区尺寸。"""
    try:
        rect = ctypes.wintypes.RECT()
        ctypes.windll.user32.SystemParametersInfoW(0x0030, 0, ctypes.byref(rect), 0)
        return int(rect.right - rect.left), int(rect.bottom - rect.top)
    except Exception:  # noqa: BLE001
        return 1920, 1080


def _get_dpi_scale() -> float:
    """获取 Windows 主显示器 DPI 缩放比例。"""
    try:
        ctypes.windll.user32.SetProcessDPIAware()
        dc = ctypes.windll.user32.GetDC(0)
        dpi = ctypes.windll.gdi32.GetDeviceCaps(dc, 88)
        ctypes.windll.user32.ReleaseDC(0, dc)
        return max(dpi / 96.0, 1.0)
    except Exception:  # noqa: BLE001
        return 1.0


def _wait_for_local_url(url: str, timeout: float = 20.0) -> bool:
    """等待本地服务可访问。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1.5) as response:
                if response.status < 500:
                    return True
        except Exception:  # noqa: BLE001
            time.sleep(0.2)
    return False


def _set_toolwindow(hwnd: int) -> None:
    """将窗口设为 toolwindow 并启用分层窗口,避免出现在任务栏。"""
    style = GetWindowLongW(hwnd, GWL_EXSTYLE)
    style = (style | WS_EX_TOOLWINDOW | WS_EX_LAYERED) & ~WS_EX_APPWINDOW
    SetWindowLongW(hwnd, GWL_EXSTYLE, style)
    SetWindowPos(
        hwnd,
        0,
        0,
        0,
        0,
        0,
        SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_FRAMECHANGED,
    )


def _apply_layered_bitmap(hwnd: int, pil_image: Image.Image, opacity: int) -> None:
    """使用 UpdateLayeredWindow 将 RGBA 图片绘制到分层窗口,支持 per-pixel alpha。"""
    width, height = pil_image.size

    # Windows DIB 需要 BGRA 格式且自底向上排列。
    r, g, b, a = pil_image.split()
    bgra = Image.merge("RGBA", (b, g, r, a))
    flipped = bgra.transpose(Image.FLIP_TOP_BOTTOM)
    img_bytes = flipped.tobytes()

    hdc_screen = GetDC(None)
    hdc_mem = CreateCompatibleDC(hdc_screen)

    bmi = _BITMAPINFO()
    bmi.bmiHeader.biSize = ctypes.sizeof(_BITMAPINFOHEADER)
    bmi.bmiHeader.biWidth = width
    bmi.bmiHeader.biHeight = height
    bmi.bmiHeader.biPlanes = 1
    bmi.bmiHeader.biBitCount = 32
    bmi.bmiHeader.biCompression = 0
    bmi.bmiHeader.biSizeImage = width * height * 4

    bits = ctypes.c_void_p()
    hbmp = CreateDIBSection(hdc_mem, ctypes.byref(bmi), DIB_RGB_COLORS, ctypes.byref(bits), None, 0)
    _memmove(bits, img_bytes, len(img_bytes))
    old_bmp = SelectObject(hdc_mem, hbmp)

    pt_src = _POINT(0, 0)
    pt_dst = _POINT(0, 0)
    size = _POINT(width, height)
    blend = _BLENDFUNCTION()
    blend.BlendOp = AC_SRC_OVER
    blend.BlendFlags = 0
    blend.SourceConstantAlpha = max(0, min(255, opacity))
    blend.AlphaFormat = AC_SRC_ALPHA

    UpdateLayeredWindow(
        hwnd, hdc_screen, ctypes.byref(pt_dst), ctypes.byref(size),
        hdc_mem, ctypes.byref(pt_src), 0, ctypes.byref(blend), ULW_ALPHA,
    )

    SelectObject(hdc_mem, old_bmp)
    DeleteObject(hbmp)
    DeleteDC(hdc_mem)
    ReleaseDC(None, hdc_screen)


def _center_window(window: tk.Toplevel, width: int, height: int) -> None:
    """将窗口居中显示。"""
    screen_w = window.winfo_screenwidth()
    screen_h = window.winfo_screenheight()
    x = (screen_w - width) // 2
    y = (screen_h - height) // 2
    window.geometry(f"{width}x{height}+{x}+{y}")


def _show_splash(image_path: Path) -> tuple[tk.Tk, tk.Toplevel, Image.Image]:
    """创建只显示图片的无边框启动图,支持 per-pixel alpha 透明。"""
    if not image_path.exists():
        raise FileNotFoundError(f"找不到启动图: {image_path}")

    dpi_scale = _get_dpi_scale()
    work_w, work_h = _get_work_area()
    # 启动图最大边占屏幕短边的 3/4,按 DPI 缩放换算为物理像素。
    logical_min = min(work_w, work_h) / dpi_scale
    target_max = max(96, int(logical_min * 3 / 4 * dpi_scale))

    root = tk.Tk()
    root.withdraw()

    # 创建窗口并立即 withdraw() 隐藏,防止 1x1 白窗口闪烁。
    splash = tk.Toplevel(root)
    splash.overrideredirect(True)
    splash.geometry("1x1")
    splash.attributes("-topmost", True)
    splash.withdraw()  # 隐藏,避免 1x1 白窗口闪烁
    splash.update_idletasks()

    # 使用 PIL 缩放到目标尺寸,保持 RGBA 以便 per-pixel alpha。
    pil_image = Image.open(image_path).convert("RGBA")
    orig_w, orig_h = pil_image.size
    scale = min(target_max / orig_w, target_max / orig_h, 1.0)
    pil_image = pil_image.resize(
        (max(1, int(orig_w * scale)), max(1, int(orig_h * scale))),
        Image.LANCZOS,
    )

    # 设为正确尺寸,建立分层窗口。
    _center_window(splash, pil_image.width, pil_image.height)
    splash.update_idletasks()
    _set_toolwindow(splash.winfo_id())
    # 初始化分层窗口内容为全透明,deiconify 时窗口完全不可见。
    _apply_layered_bitmap(splash.winfo_id(), pil_image, 0)

    return root, splash, pil_image


def _fade_layered(
    window: tk.Toplevel,
    pil_image: Image.Image,
    start: int,
    end: int,
    step: int,
    delay_ms: int,
) -> None:
    """对分层启动图窗口执行淡入或淡出（0-255）。"""
    # 首次调用时 deiconify,确保窗口真正出现后才开始绘制,避免闪烁。
    window.deiconify()
    alpha = start
    direction = 1 if end >= start else -1
    while (direction > 0 and alpha <= end) or (direction < 0 and alpha >= end):
        try:
            _apply_layered_bitmap(window.winfo_id(), pil_image, alpha)
            window.update_idletasks()
            window.update()
        except tk.TclError:
            return
        alpha += step * direction
        time.sleep(delay_ms / 1000)
    try:
        _apply_layered_bitmap(window.winfo_id(), pil_image, end)
    except tk.TclError:
        pass


def _launch_backend(app, state: LaunchState) -> None:
    """后台启动 Gradio 服务。"""
    try:
        _, local_url, _ = app.launch(
            share=False,
            inbrowser=False,
            prevent_thread_lock=True,
            server_name="127.0.0.1",
            allowed_paths=[str(config.OUTPUT_DIR), str(config.PROJECT_ROOT)],
        )
        if not _wait_for_local_url(local_url):
            raise RuntimeError(f"本地服务未在预期时间内就绪: {local_url}")

        state.local_url = local_url
        state.ready = True
    except Exception as exc:  # noqa: BLE001
        state.error = str(exc)
        state.failed = True


def _open_native_window(local_url: str, scale: float) -> None:
    """打开 pywebview 原生主窗口。"""
    # 允许 pywebview 内触发文件下载(如 Gradio 的 File 组件)。
    webview.settings["ALLOW_DOWNLOADS"] = True

    work_w, work_h, dpi_scale = webui._get_logical_work_area()

    ratio = max(0.1, min(1.0, scale))
    width = int(work_w * ratio)
    height = int(work_h * ratio)
    min_w = int(width * 0.8)
    min_h = int(height * 0.8)

    print(
        f"[AI_MIDI] DPI scale={dpi_scale:.2f}, work area={work_w}x{work_h} "
        f"(logical), window={width}x{height} (logical), ratio={ratio:.2f}"
    )

    webview.create_window(
        webui.WINDOW_TITLE,
        local_url,
        width=width,
        height=height,
        min_size=(min_w, min_h),
    )
    webview.start(
        webui._set_native_window_icon,
        args=(webui.WINDOW_TITLE, webui.SPLASH_IMAGE),
    )


def _wait_with_splash(
    state: LaunchState, splash: tk.Toplevel, root: tk.Tk, pil_image: Image.Image
) -> None:
    """等待后台启动完成。"""
    def poll() -> None:
        if state.ready:
            _fade_layered(splash, pil_image, start=255, end=0, step=20, delay_ms=16)
            root.quit()
            return

        if state.failed:
            _fade_layered(splash, pil_image, start=255, end=0, step=20, delay_ms=16)
            root.quit()
            return

        root.after(120, poll)

    _fade_layered(splash, pil_image, start=0, end=255, step=20, delay_ms=16)
    root.after(120, poll)
    root.mainloop()


def main() -> None:
    parser = argparse.ArgumentParser(description="AI_MIDI Launcher")
    parser.add_argument(
        "--browser",
        action="store_true",
        help="在系统默认浏览器中打开,而不是使用原生窗口",
    )
    parser.add_argument(
        "--scale",
        type=float,
        default=0.8,
        metavar="RATIO",
        help="原生窗口占屏幕工作区的比例(0.0-1.0),默认 0.8",
    )
    args = parser.parse_args()

    app = webui.build_ui()
    state = LaunchState()

    root, splash, pil_image = _show_splash(SPLASH_IMAGE)

    worker = threading.Thread(target=_launch_backend, args=(app, state), daemon=True)
    worker.start()

    try:
        _wait_with_splash(state, splash, root, pil_image)
    finally:
        try:
            splash.destroy()
        except tk.TclError:
            pass
        try:
            root.destroy()
        except tk.TclError:
            pass

    if state.failed:
        messagebox.showerror("AI_MIDI", f"启动失败:\n{state.error}")
        raise SystemExit(1)

    if args.browser:
        webbrowser.open(state.local_url)
        return

    _open_native_window(state.local_url, args.scale)


if __name__ == "__main__":
    main()
