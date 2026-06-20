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

from PIL import Image, ImageTk
import webview

import webui


# 启动图单独使用 Go.png,不影响 pywebview 窗口图标(仍用 webui.SPLASH_IMAGE / app_icon.ico)
SPLASH_IMAGE = Path(__file__).with_name("Go.png")


GWL_EXSTYLE = -20
WS_EX_TOOLWINDOW = 0x00000080
WS_EX_APPWINDOW = 0x00040000
SWP_NOMOVE = 0x0002
SWP_NOSIZE = 0x0001
SWP_NOZORDER = 0x0004
SWP_FRAMECHANGED = 0x0020


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
    """将窗口设为 toolwindow,避免出现在任务栏。"""
    style = ctypes.windll.user32.GetWindowLongW(hwnd, GWL_EXSTYLE)
    style = (style | WS_EX_TOOLWINDOW) & ~WS_EX_APPWINDOW
    ctypes.windll.user32.SetWindowLongW(hwnd, GWL_EXSTYLE, style)
    ctypes.windll.user32.SetWindowPos(
        hwnd,
        0,
        0,
        0,
        0,
        0,
        SWP_NOMOVE | SWP_NOSIZE | SWP_NOZORDER | SWP_FRAMECHANGED,
    )


def _center_window(window: tk.Toplevel, width: int, height: int) -> None:
    """将窗口居中显示。"""
    screen_w = window.winfo_screenwidth()
    screen_h = window.winfo_screenheight()
    x = (screen_w - width) // 2
    y = (screen_h - height) // 2
    window.geometry(f"{width}x{height}+{x}+{y}")


def _fade(window: tk.Toplevel, start: float, end: float, step: float, delay_ms: int) -> None:
    """对启动图窗口执行淡入或淡出。"""
    alpha = start
    direction = 1 if end >= start else -1
    while (direction > 0 and alpha <= end) or (direction < 0 and alpha >= end):
        try:
            window.attributes("-alpha", max(0.0, min(1.0, alpha)))
            window.update_idletasks()
            window.update()
        except tk.TclError:
            return
        alpha += step * direction
        time.sleep(delay_ms / 1000)
    try:
        window.attributes("-alpha", end)
    except tk.TclError:
        pass


def _show_splash(image_path: Path) -> tuple[tk.Tk, tk.Toplevel]:
    """创建只显示图片的无边框启动图。"""
    if not image_path.exists():
        raise FileNotFoundError(f"找不到启动图: {image_path}")

    dpi_scale = _get_dpi_scale()
    work_w, work_h = _get_work_area()
    # 启动图最大边占屏幕短边的 3/4,按 DPI 缩放换算为物理像素。
    logical_min = min(work_w, work_h) / dpi_scale
    target_max = max(96, int(logical_min * 3 / 4 * dpi_scale))

    root = tk.Tk()
    root.withdraw()

    splash = tk.Toplevel(root)
    splash.overrideredirect(True)
    splash.attributes("-topmost", True)
    splash.attributes("-alpha", 0.0)
    # 用洋红色作为透明色,让 PNG 透明区域直接看到桌面。
    transparent_color = "magenta"
    splash.configure(bg=transparent_color)
    splash.attributes("-transparentcolor", transparent_color)

    # 使用 PIL 做任意比例缩放,比 tkinter 的整数 subsample 更精确。
    # 显式转成 RGBA,防止 PNG 透明通道在缩放后变成白色。
    pil_image = Image.open(image_path).convert("RGBA")
    orig_w, orig_h = pil_image.size
    scale = min(target_max / orig_w, target_max / orig_h, 1.0)
    new_w = max(1, int(orig_w * scale))
    new_h = max(1, int(orig_h * scale))
    pil_image = pil_image.resize((new_w, new_h), Image.LANCZOS)
    photo = ImageTk.PhotoImage(pil_image)
    splash._photo = photo  # type: ignore[attr-defined]

    label = tk.Label(
        splash,
        image=photo,
        borderwidth=0,
        highlightthickness=0,
        bg=transparent_color,
    )
    label.pack()

    splash.update_idletasks()
    _center_window(splash, photo.width(), photo.height())
    splash.update_idletasks()
    _set_toolwindow(splash.winfo_id())

    return root, splash


def _launch_backend(app, state: LaunchState) -> None:
    """后台启动 Gradio 服务。"""
    try:
        _, local_url, _ = app.launch(
            share=False,
            inbrowser=False,
            prevent_thread_lock=True,
            server_name="127.0.0.1",
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


def _wait_with_splash(state: LaunchState, splash: tk.Toplevel, root: tk.Tk) -> None:
    """等待后台启动完成。"""
    def poll() -> None:
        if state.ready:
            _fade(splash, start=1.0, end=0.0, step=0.08, delay_ms=16)
            root.quit()
            return

        if state.failed:
            _fade(splash, start=1.0, end=0.0, step=0.08, delay_ms=16)
            root.quit()
            return

        root.after(120, poll)

    _fade(splash, start=0.0, end=1.0, step=0.08, delay_ms=16)
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

    root, splash = _show_splash(SPLASH_IMAGE)

    worker = threading.Thread(target=_launch_backend, args=(app, state), daemon=True)
    worker.start()

    try:
        _wait_with_splash(state, splash, root)
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
