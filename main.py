"""AI_MIDI 入口。

启动 FastAPI 服务（127.0.0.1:7860）并承载蓝图风静态前端；
默认以 pywebview 原生窗口打开，--browser 时使用系统浏览器。

用法:
    python main.py
    python main.py --browser
    python main.py --scale 0.9
"""
import argparse
import logging
import struct
import threading
import time
from pathlib import Path

import config

logger = logging.getLogger("ai_midi")

WINDOW_TITLE = "AI_MIDI · AI 编曲助手"
SPLASH_IMAGE = Path(__file__).with_name("splash.png")

WM_SETICON = 0x0080
ICON_SMALL = 0
ICON_BIG = 1
IMAGE_ICON = 1
LR_LOADFROMFILE = 0x0010
LR_DEFAULTSIZE = 0x0040
GCLP_HICON = -14
GCLP_HICONSM = -34

SERVER_URL = f"http://127.0.0.1:{config.SERVER_PORT}"


def _start_server() -> None:
    """在后台线程启动 uvicorn 服务。"""
    import uvicorn

    from server import app

    uvicorn.run(
        app,
        host="127.0.0.1",
        port=config.SERVER_PORT,
        log_level="warning",
    )


def _wait_for_server(timeout: float = 15.0) -> bool:
    """轮询等待 FastAPI 服务就绪。"""
    import urllib.request

    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            urllib.request.urlopen(f"{SERVER_URL}/api/health", timeout=0.5)
            return True
        except OSError:
            time.sleep(0.3)
    return False


# ===== Windows 原生窗口相关（迁移自原 webui.py） =====

def _get_dpi_scale() -> float:
    """获取 Windows 主显示器的 DPI 缩放比例(以 96 DPI 为基准)。"""
    try:
        import ctypes

        ctypes.windll.user32.SetProcessDPIAware()
        dc = ctypes.windll.user32.GetDC(0)
        dpi = ctypes.windll.gdi32.GetDeviceCaps(dc, 88)  # LOGPIXELSX
        ctypes.windll.user32.ReleaseDC(0, dc)
        return max(dpi / 96.0, 1.0)
    except (OSError, AttributeError, ImportError):
        return 1.0


def _get_work_area() -> tuple[int, int]:
    """获取 Windows 主显示器工作区尺寸(不含任务栏),单位为逻辑像素。"""
    try:
        import ctypes
        from ctypes.wintypes import RECT

        rect = RECT()
        ctypes.windll.user32.SystemParametersInfoW(0x0030, 0, ctypes.byref(rect), 0)
        return int(rect.right - rect.left), int(rect.bottom - rect.top)
    except (OSError, AttributeError, ImportError):
        return 1920, 1080


def _get_logical_work_area() -> tuple[int, int, float]:
    """获取适合 pywebview 的逻辑工作区尺寸和当前 DPI 缩放。"""
    dpi_scale = _get_dpi_scale()
    work_w, work_h = _get_work_area()
    logical_w = max(1, int(round(work_w / dpi_scale)))
    logical_h = max(1, int(round(work_h / dpi_scale)))
    return logical_w, logical_h, dpi_scale


def _set_current_process_app_id(app_id: str = WINDOW_TITLE) -> None:
    """为当前进程设置显式 AppUserModelID,帮助任务栏正确区分应用。"""
    try:
        import ctypes

        ctypes.windll.shell32.SetCurrentProcessExplicitAppUserModelID(app_id)
    except (OSError, AttributeError, ImportError):
        pass


def _png_size(png_bytes: bytes) -> tuple[int, int]:
    """读取 PNG 的原始宽高。"""
    if png_bytes[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError("不是有效的 PNG 文件")
    return struct.unpack(">II", png_bytes[16:24])


def _is_usable_icon_file(icon_path: Path, source_path: Path) -> bool:
    """检查现有 ico 是否足够新且包含多尺寸条目。"""
    if not icon_path.exists():
        return False
    if icon_path.stat().st_mtime < source_path.stat().st_mtime:
        return False
    try:
        icon_bytes = icon_path.read_bytes()
        if len(icon_bytes) < 6:
            return False
        image_count = struct.unpack("<H", icon_bytes[4:6])[0]
        return image_count >= 4
    except (OSError, ValueError, struct.error):
        return False


def _ensure_icon_file(image_path: Path, icon_path: Path | None = None) -> Path:
    """基于 PNG 生成 Windows 可用的 ICO 文件。"""
    if image_path.suffix.lower() == ".ico":
        return image_path
    if image_path.suffix.lower() != ".png":
        raise ValueError(f"Windows 原生窗口图标仅支持 .ico 或由 .png 生成,当前为: {image_path}")

    icon_path = icon_path or image_path.with_suffix(".ico")
    if _is_usable_icon_file(icon_path, image_path):
        return icon_path

    try:
        import clr

        clr.AddReference("System.Drawing")
        clr.AddReference("System")
        from System.Drawing import Bitmap, Size
        from System.Drawing.Imaging import ImageFormat
        from System.IO import MemoryStream

        bitmap = Bitmap(str(image_path))
        sizes = [256, 128, 64, 48, 32, 16]
        icon_images: list[tuple[int, bytes]] = []

        try:
            for size in sizes:
                resized = Bitmap(bitmap, Size(size, size))
                stream = MemoryStream()
                try:
                    resized.Save(stream, ImageFormat.Png)
                    icon_images.append((size, bytes(stream.ToArray())))
                finally:
                    stream.Dispose()
                    resized.Dispose()
        finally:
            bitmap.Dispose()

        header = struct.pack("<HHH", 0, 1, len(icon_images))
        entries: list[bytes] = []
        payloads: list[bytes] = []
        offset = 6 + 16 * len(icon_images)

        for size, png_bytes in icon_images:
            size_byte = 0 if size >= 256 else size
            entries.append(
                struct.pack(
                    "<BBBBHHII",
                    size_byte,
                    size_byte,
                    0,
                    0,
                    1,
                    32,
                    len(png_bytes),
                    offset,
                )
            )
            payloads.append(png_bytes)
            offset += len(png_bytes)

        icon_path.write_bytes(header + b"".join(entries) + b"".join(payloads))
        return icon_path
    except Exception:  # noqa: BLE001
        png_bytes = image_path.read_bytes()
        width, height = _png_size(png_bytes)
        width_byte = 0 if width >= 256 else width
        height_byte = 0 if height >= 256 else height

        header = struct.pack("<HHH", 0, 1, 1)
        entry = struct.pack(
            "<BBBBHHII",
            width_byte,
            height_byte,
            0,
            0,
            1,
            32,
            len(png_bytes),
            22,
        )
        icon_path.write_bytes(header + entry + png_bytes)
        return icon_path


def _set_native_window_icon(window_title: str, image_path: Path, timeout: float = 5.0) -> None:
    """给 Windows 下的 pywebview 主窗口设置图标。"""
    _set_current_process_app_id()
    window_icon_path = config.PROJECT_ROOT / "window_icon.ico"
    icon_path = _ensure_icon_file(image_path, window_icon_path)
    deadline = time.time() + timeout

    while time.time() < deadline:
        try:
            import clr
            import webview

            clr.AddReference("System.Drawing")
            clr.AddReference("System")
            from System import Action
            from System.Drawing import Icon as DrawingIcon

            for window in getattr(webview, "windows", []):
                if getattr(window, "title", "") != window_title:
                    continue
                native = getattr(window, "native", None)
                if native is None:
                    continue

                icon = DrawingIcon(str(icon_path))

                def _apply_icon() -> None:
                    native.Icon = icon
                    native.ShowIcon = True
                    native.ShowInTaskbar = True

                if getattr(native, "InvokeRequired", False):
                    native.Invoke(Action(_apply_icon))
                else:
                    _apply_icon()
                return
        except Exception:  # noqa: BLE001
            pass
        time.sleep(0.1)

    try:
        import ctypes
    except ImportError:
        return

    user32 = ctypes.windll.user32
    set_class_long = getattr(user32, "SetClassLongPtrW", user32.SetClassLongW)

    hwnd = 0
    while time.time() < deadline:
        hwnd = user32.FindWindowW(None, window_title)
        if hwnd:
            break
        time.sleep(0.1)

    if not hwnd:
        return

    small_icon = user32.LoadImageW(
        None,
        str(icon_path),
        IMAGE_ICON,
        16,
        16,
        LR_LOADFROMFILE | LR_DEFAULTSIZE,
    )
    big_icon = user32.LoadImageW(
        None,
        str(icon_path),
        IMAGE_ICON,
        32,
        32,
        LR_LOADFROMFILE | LR_DEFAULTSIZE,
    )

    if big_icon:
        set_class_long(hwnd, GCLP_HICON, big_icon)
        user32.SendMessageW(hwnd, WM_SETICON, ICON_BIG, big_icon)
    if small_icon:
        set_class_long(hwnd, GCLP_HICONSM, small_icon)
        user32.SendMessageW(hwnd, WM_SETICON, ICON_SMALL, small_icon)


def main() -> None:
    parser = argparse.ArgumentParser(description="AI_MIDI Web UI")
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

    _set_current_process_app_id()

    # 后台启动 FastAPI 服务
    server_thread = threading.Thread(target=_start_server, daemon=True)
    server_thread.start()
    if not _wait_for_server(timeout=20.0):
        logger.error("服务启动失败或超时: %s", SERVER_URL)
        raise SystemExit(1)

    if args.browser:
        import webbrowser

        webbrowser.open(SERVER_URL)
        # 浏览器模式下主线程保持存活（后台线程为 daemon，直接返回会退出进程）
        while True:
            time.sleep(3600)
        return

    # 原生窗口模式：pywebview 承载页面
    import webview

    # 允许 pywebview 内触发文件下载
    webview.settings["ALLOW_DOWNLOADS"] = True

    work_w, work_h, dpi_scale = _get_logical_work_area()

    # 目标:窗口逻辑尺寸占屏幕工作区的指定比例
    ratio = max(0.1, min(1.0, args.scale))
    width = int(work_w * ratio)
    height = int(work_h * ratio)
    min_w = int(width * 0.8)
    min_h = int(height * 0.8)

    print(f"[AI_MIDI] DPI scale={dpi_scale:.2f}, work area={work_w}x{work_h} "
          f"(logical), window={width}x{height} (logical), ratio={ratio:.2f}")

    webview.create_window(
        WINDOW_TITLE,
        SERVER_URL,
        width=width,
        height=height,
        min_size=(min_w, min_h),
    )
    webview.start(
        _set_native_window_icon,
        args=(WINDOW_TITLE, SPLASH_IMAGE),
    )


if __name__ == "__main__":
    main()
