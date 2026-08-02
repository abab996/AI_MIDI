"""AI_MIDI Web UI 入口。

基于 Gradio 实现,作为 gui.py 的跨平台替代,可在浏览器中操作。

用法:
    python webui.py
"""
import logging
import os
import shutil
import struct
import threading
import time
import urllib.request
from pathlib import Path

import gradio as gr

import ai_api
import config
import get
import out
from out import EmptyNoteTableError
from webui_components import (
    FUNC_ADD_CHORD,
    FUNC_MELISMA,
    FUNC_OTHER,
    FUNC_TRANSLATE,
    _build_function_section,
    _build_header,
    _build_mode_section,
    _build_result_section,
    _build_settings_section,
    _build_tuning_section,
    _build_upload_section,
    _FUNC_FIELDS,
)

logger = logging.getLogger("ai_midi")


WINDOW_TITLE = "AI_MIDI · AI 编曲助手"
SPLASH_IMAGE = Path(__file__).with_name("splash.png")
APP_ICON_FILE = Path(__file__).with_name("app_icon.ico")

WM_SETICON = 0x0080
ICON_SMALL = 0
ICON_BIG = 1
IMAGE_ICON = 1
LR_LOADFROMFILE = 0x0010
LR_DEFAULTSIZE = 0x0040
GCLP_HICON = -14
GCLP_HICONSM = -34

# ===== 多轮对话相关 =====
CHAT_PORT = 7861
CHAT_URL = f"http://127.0.0.1:{CHAT_PORT}"
_chat_thread: threading.Thread | None = None
_chat_started = False
_chat_launch_error: str | None = None
_chat_launch_lock = threading.Lock()


def _is_chat_running() -> bool:
    """检查多轮对话服务是否已在运行。"""
    try:
        urllib.request.urlopen(CHAT_URL, timeout=0.5)
        return True
    except OSError:
        return False


def _wait_for_chat_ready(timeout: float = 15.0) -> bool:
    """轮询等待多轮对话服务就绪。"""
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            urllib.request.urlopen(CHAT_URL, timeout=0.5)
            return True
        except OSError:
            time.sleep(0.3)
    return False


_CHAT_WINDOW_TITLE = "AI_MIDI · 多轮对话"


def _is_native_window_mode() -> bool:
    """主界面是否以 pywebview 原生窗口模式运行（而非 --browser 浏览器模式）。"""
    try:
        import webview
        return bool(webview.windows)
    except Exception:  # noqa: BLE001
        return False


def _open_chat_window() -> None:
    """多轮对话服务就绪后打开窗口。

    与主界面一致优先使用 pywebview 原生窗口；若已有同名对话窗口则激活它
    而非新开，避免弹出多个窗口。当主界面以浏览器模式运行（pywebview 未
    启动）时，回退到系统默认浏览器打开。
    """
    if _is_native_window_mode():
        try:
            import webview
            # 已有对话窗口则激活，不重复开窗
            for w in webview.windows:
                if getattr(w, "title", "") == _CHAT_WINDOW_TITLE:
                    try:
                        w.show()
                    except Exception:  # noqa: BLE001
                        pass
                    return
            work_w, work_h, _ = _get_logical_work_area()
            ratio = 0.72
            width = max(640, int(work_w * ratio))
            height = max(480, int(work_h * ratio))
            webview.create_window(
                _CHAT_WINDOW_TITLE,
                CHAT_URL,
                width=width,
                height=height,
                min_size=(int(width * 0.8), int(height * 0.8)),
            )
            return
        except Exception:  # noqa: BLE001
            logger.exception("pywebview 打开多轮对话窗口失败，回退浏览器")

    import webbrowser
    webbrowser.open(CHAT_URL)


def _start_chat_server() -> None:
    """在后台启动多轮对话 Gradio 服务并等待就绪。"""
    global _chat_launch_error, _chat_started
    try:
        from chat_ui import build_chat_ui

        app = build_chat_ui()
        app.launch(
            prevent_thread_lock=True,
            server_name="127.0.0.1",
            server_port=CHAT_PORT,
            share=False,
            inbrowser=False,
        )
        if not _wait_for_chat_ready(timeout=10.0):
            _chat_launch_error = "服务启动超时"
            _chat_started = False
        else:
            _chat_launch_error = None
            _chat_started = True
            _open_chat_window()
    except OSError as e:
        if "Address already in use" in str(e):
            if _wait_for_chat_ready(timeout=3.0):
                _chat_launch_error = None
                _chat_started = True
                _open_chat_window()
            else:
                _chat_launch_error = f"端口 {CHAT_PORT} 被占用且服务不可用"
                _chat_started = False
        else:
            _chat_launch_error = str(e)
            _chat_started = False
    except Exception as e:  # noqa: BLE001
        _chat_launch_error = str(e)
        _chat_started = False
        logger.exception("启动多轮对话服务失败")


def launch_chat() -> str:
    """启动多轮对话窗口（供 Gradio 按钮回调使用）。"""
    global _chat_thread, _chat_started, _chat_launch_error

    with _chat_launch_lock:
        _chat_launch_error = None

        if _is_chat_running():
            _chat_started = True
            _open_chat_window()
            return f"多轮对话窗口已打开：{CHAT_URL}"

        if _chat_started and _chat_thread and _chat_thread.is_alive():
            return f"正在启动多轮对话窗口… {CHAT_URL}"

        _chat_thread = threading.Thread(target=_start_chat_server, daemon=True)
        _chat_thread.start()

    return f"正在启动多轮对话窗口… {CHAT_URL}"

# ===== 用户设置持久化(委托给 config 统一管理) =====
def _load_settings() -> dict:
    """从 settings.json 加载用户设置,失败时返回空字典。"""
    return config.load_settings()


def _save_settings(
    api_key: str,
    base_url: str,
    api_path: str,
    model: str,
    max_tokens: int | float | None,
    max_completion_tokens: int | float | None,
    reasoning_effort: str,
    thinking_enabled: bool,
) -> str:
    """保存用户设置到本地 JSON 文件。"""
    # 在保存前验证 base_url，防止恶意域名写入 settings.json
    try:
        config.validate_base_url(base_url.strip(), api_path.strip())
    except ValueError as e:
        return f"✗ 保存失败：{e}"

    def _to_int(value) -> int | None:
        if value is None or value == "":
            return None
        try:
            return int(value)
        except (ValueError, TypeError):
            return None

    settings = {
        "api_key": api_key.strip(),
        "base_url": base_url.strip(),
        "api_path": api_path.strip(),
        "model": model,
        "max_tokens": _to_int(max_tokens),
        "max_completion_tokens": _to_int(max_completion_tokens),
        "reasoning_effort": reasoning_effort,
        "thinking_enabled": thinking_enabled,
    }
    config.save_settings(settings)
    return "✓ 配置已保存"


def _note_to_text(note_table: list[str]) -> str:
    """把 note_table 列表合并成字符串。"""
    return "\n".join(note_table)


def _fetch_models(api_key: str, base_url: str, api_path: str = "") -> tuple[list[str], str]:
    """从 API 服务拉取模型列表,返回 (模型 id 列表, 状态信息)。"""
    key = api_key.strip() or config.get_api_key()
    url = base_url.strip() or config.BASE_URL
    path = api_path.strip()

    if not key:
        return [], "⚠ 请先填写并保存 API Key"

    try:
        full_url = config.validate_base_url(url, path)
    except ValueError:
        logger.exception("base_url 验证失败")
        return [], "✗ 配置错误,请检查 Base URL 与 API 路径。"

    try:
        client = ai_api.get_client(api_key=key, base_url=full_url)
        models = client.models.list()
        ids = sorted([m.id for m in models.data])
        if not ids:
            return [], "⚠ 未获取到任何模型"
        return ids, f"✓ 已获取 {len(ids)} 个模型"
    # intentional: AI API can raise various exception types (network, auth, rate-limit, response parse)
    except Exception:  # noqa: BLE001
        logger.exception("获取模型列表失败")
        return [], "✗ 获取模型失败,请检查网络或 API Key。"


def _parse_midi(file_path: str | None) -> tuple[str, list[str]]:
    """解析上传的 MIDI 文件,返回 (状态文本, note_table 列表)。"""
    if not file_path:
        return "请先上传 MIDI 文件。", []

    src = Path(file_path)
    if not src.exists():
        return f"文件不存在: {file_path}", []

    # 同步到默认输入路径,方便下游模块直接读取
    os.makedirs(config.INPUT_MIDI.parent, exist_ok=True)
    shutil.copy2(src, config.INPUT_MIDI)

    try:
        note_table = get.get_note(str(config.INPUT_MIDI), save_to_file=False)
    except (OSError, ValueError):
        logger.exception("MIDI 解析失败")
        return "解析失败,请检查 MIDI 文件后重试。", []

    if not note_table:
        return "未解析出音符,请检查 MIDI 文件是否有效。", []

    return f"✓ 已解析 {len(note_table)} 个音符", note_table


def _update_func_visibility(func: str) -> tuple[dict, dict, dict, dict]:
    """根据选择的功能返回各字段的 visible 更新。"""
    rows = _FUNC_FIELDS[func]
    return (
        gr.update(visible=rows["lyrics"]),
        gr.update(visible=rows["lang"]),   # 语言行整体
        gr.update(visible=rows["note_sw"]),
        gr.update(visible=rows["req"]),
    )


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
        # 不设置 DPI 感知,让 SPI_GETWORKAREA 返回逻辑像素(与 pywebview 的 width/height 单位一致)
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
    # intentional: fallback path must catch any CLR/.NET interop failure
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
    # 窗口图标单独生成到 window_icon.ico,避免覆盖 APP_ICON_FILE,
    # 也避免启动图修改影响任务栏/标题栏图标。
    window_icon_path = config.PROJECT_ROOT / "window_icon.ico"
    icon_path = _ensure_icon_file(image_path, window_icon_path)
    deadline = time.time() + timeout

    # WinForms 的 Form.Icon 会同步影响标题栏和任务栏图标。
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
    # intentional: retry loop for CLR interop, any transient failure should retry
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


def _validate_bpm(bpm) -> str:
    """验证 BPM 值,返回字符串形式的合法 BPM。"""
    try:
        bpm = int(bpm)
        return str(max(config.BPM_MIN, min(config.BPM_MAX, bpm)))
    except (ValueError, TypeError):
        return str(config.DEFAULT_BPM)


def _validate_time_signature(ts: str) -> str:
    import re
    if re.match(r'^\d+/\d+$', ts.strip()):
        return ts.strip()
    return config.DEFAULT_TIME_SIGNATURE


def _is_valid_time_signature(ts: str) -> bool:
    """检查拍号格式是否合法 (如 4/4, 3/4)。"""
    import re
    return bool(re.match(r'^\d+/\d+$', ts.strip()))


def _validate_int_param(value, default, min_val, max_val):
    try:
        v = int(value)
        return max(min_val, min(max_val, v))
    except (ValueError, TypeError):
        return default


def _run_task(
    func: str,
    note_table: list[str],
    bpm: str,
    time_signature: str,
    lyrics: str,
    original_language: str,
    target_language: str,
    note_output: bool,
    requirements: str,
    api_key: str,
    base_url: str,
    api_path: str,
    model: str,
    max_tokens: int | None,
    max_completion_tokens: int | None,
    reasoning_effort: str,
    thinking_enabled: bool,
    progress: gr.Progress = gr.Progress(),
) -> tuple[str, str | None, str]:
    """调用 AI API 并返回 (结果文本, 可下载文件路径, 状态文本)。"""
    start_time = time.time()

    def _elapsed(msg: str) -> str:
        return f"{msg}（耗时 {time.time() - start_time:.2f} 秒）"

    # 除"其他要求"外,其余功能都需要先解析 MIDI
    if func != FUNC_OTHER and not note_table:
        return "", None, _elapsed("⚠ 请先解析 MIDI 文件。")

    progress(0.2, desc="解析 MIDI")

    # API key:输入框优先,为空时回退到 settings.json 中保存的值
    effective_api_key = api_key.strip() or config.get_api_key()
    if not effective_api_key:
        return "", None, _elapsed("⚠ 请先在设置页填写并保存 API Key。")

    bpm = _validate_bpm(bpm if not isinstance(bpm, str) else bpm.strip() or str(config.DEFAULT_BPM))
    ts_raw = time_signature if isinstance(time_signature, str) else str(time_signature)
    if not _is_valid_time_signature(ts_raw):
        gr.Warning(f"拍号格式无效 '{ts_raw}',已回退到 {config.DEFAULT_TIME_SIGNATURE}")
    time_signature = _validate_time_signature(ts_raw.strip() or config.DEFAULT_TIME_SIGNATURE)
    note_text = _note_to_text(note_table) if note_table else ""

    # 组装公共 API 参数,空值不传入,让 ai_api 使用默认值
    api_kwargs: dict = {}
    if effective_api_key:
        api_kwargs["api_key"] = effective_api_key
    if base_url.strip() or api_path.strip():
        try:
            api_kwargs["base_url"] = config.validate_base_url(base_url.strip(), api_path.strip())
        except ValueError:
            logger.exception("base_url/api_path 验证失败")
            return "", None, _elapsed("✗ 配置错误,请检查 Base URL 与 API 路径。")
    if model.strip():
        api_kwargs["model"] = model.strip()
    if max_tokens:
        api_kwargs["max_tokens"] = _validate_int_param(max_tokens, config.DEFAULT_MAX_TOKENS, config.MAX_TOKENS_MIN, config.MAX_TOKENS_MAX)
    if max_completion_tokens:
        api_kwargs["max_completion_tokens"] = _validate_int_param(max_completion_tokens, config.DEFAULT_MAX_TOKENS, config.MAX_TOKENS_MIN, config.MAX_TOKENS_MAX)
    if reasoning_effort.strip():
        api_kwargs["reasoning_effort"] = reasoning_effort.strip()
    api_kwargs["thinking_enabled"] = thinking_enabled

    progress(0.5, desc="调用 AI")

    try:
        if func == FUNC_ADD_CHORD:
            result = ai_api.add_chord(
                note_text, bpm, time_signature, requirements, **api_kwargs
            )

        elif func == FUNC_TRANSLATE:
            if not original_language.strip() or not target_language.strip():
                return "", None, _elapsed("⚠ 请填写原语言和目标语言。")
            result = ai_api.translate_lyrics(
                note_text, lyrics, bpm, time_signature,
                original_language, target_language, **api_kwargs
            )

        elif func == FUNC_MELISMA:
            result = ai_api.design_melisma(
                note_text, lyrics, bpm, time_signature, requirements, **api_kwargs
            )

        else:  # FUNC_OTHER
            if not requirements.strip():
                return "", None, _elapsed("⚠ 请填写具体要求。")
            result = ai_api.other_requirements(
                note_text, lyrics, bpm, time_signature,
                requirements, note_output, **api_kwargs
            )
    # intentional: AI API call encompasses network, auth, rate-limit, and response parsing errors
    except Exception:  # noqa: BLE001
        logger.exception("AI API 调用失败")
        return "", None, _elapsed("✗ 调用失败,请稍后重试。")

    if not result:
        return "", None, _elapsed("✗ AI 未返回内容或调用失败。")

    progress(0.9, desc="保存结果")

    # 保存结果
    os.makedirs(config.OUTPUT_DIR, exist_ok=True)
    download_path: str | None = None
    status_msg = ""

    try:
        if func == FUNC_TRANSLATE:
            save_path = config.OUTPUT_DIR / "translated_lyrics.txt"
            save_path.write_text(result, encoding="utf-8")
            download_path = str(save_path)
            status_msg = "✓ 歌词已保存"

        elif func == FUNC_OTHER and not note_output:
            save_path = config.OUTPUT_DIR / "other_requirements_result.txt"
            save_path.write_text(result, encoding="utf-8")
            download_path = str(save_path)
            status_msg = "✓ 结果已保存"

        else:
            out.out_note(result, bpm)
            download_path = str(config.OUTPUT_MIDI)
            status_msg = f"✓ MIDI 已生成: {config.OUTPUT_MIDI.name}"

    except EmptyNoteTableError:
        return result, None, _elapsed("⚠ AI 回复中未解析出有效音符，请检查 AI 输出格式。")
    except (OSError, ValueError):
        logger.exception("保存结果失败")
        return result, None, _elapsed("✓ AI 返回结果,但保存失败,请重试。")

    return result, [download_path] if download_path else None, _elapsed(status_msg)


def build_ui() -> gr.Blocks:
    """构建并返回 Gradio 应用 (薄编排层, 组件由 webui_components 构建)。"""
    settings = _load_settings()

    with gr.Blocks(title="AI_MIDI · AI 编曲助手") as app:
        _build_header()
        note_table_state = gr.State(value=[])

        with gr.Tabs():
            with gr.Tab("处理"):
                with gr.Row():
                    with gr.Column(scale=1):
                        midi_file, parse_btn, parse_status = _build_upload_section()
                        (func_selector, lyrics_box, lang_row, orig_lang_input,
                         target_lang_input, note_sw, req_box) = _build_function_section()
                        bpm_input, timesig_input = _build_tuning_section()
                        start_btn = gr.Button("▶ 开始", variant="primary")
                        status_text = gr.Textbox(label="状态", interactive=False)
                    with gr.Column(scale=2):
                        result_box, download_file = _build_result_section()

            with gr.Tab("设置"):
                (api_key_input, show_key_sw, base_url_input, api_path_input, model_input,
                 refresh_model_btn, model_status, max_tokens_input,
                 max_completion_tokens_input, reasoning_effort_input,
                 thinking_enabled_input, save_cfg_btn, save_cfg_status) = _build_settings_section(settings)

            with gr.Tab("模式"):
                launch_chat_btn, chat_status = _build_mode_section()

        # ── 事件绑定 ──
        parse_btn.click(
            fn=_parse_midi,
            inputs=midi_file,
            outputs=[parse_status, note_table_state],
        )
        midi_file.upload(
            fn=_parse_midi,
            inputs=[midi_file],
            outputs=[parse_status, note_table_state],
        )
        func_selector.change(
            fn=_update_func_visibility,
            inputs=func_selector,
            outputs=[lyrics_box, lang_row, note_sw, req_box],
        )
        show_key_sw.change(
            fn=lambda show: gr.update(type="text" if show else "password"),
            inputs=show_key_sw,
            outputs=api_key_input,
        )
        def _refresh_models(api_key: str, base_url: str, api_path: str) -> tuple[dict, str]:
            ids, msg = _fetch_models(api_key, base_url, api_path)
            if ids:
                gr.Info("模型列表已刷新")
            else:
                gr.Warning(f"刷新失败: {msg}")
            return gr.update(choices=ids), msg

        refresh_model_btn.click(
            fn=_refresh_models,
            inputs=[api_key_input, base_url_input, api_path_input],
            outputs=[model_input, model_status],
        )
        save_cfg_btn.click(
            fn=_save_settings,
            inputs=[api_key_input, base_url_input, api_path_input, model_input,
                    max_tokens_input, max_completion_tokens_input,
                    reasoning_effort_input, thinking_enabled_input],
            outputs=save_cfg_status,
        )
        start_btn.click(
            fn=_run_task,
            inputs=[func_selector, note_table_state, bpm_input, timesig_input,
                    lyrics_box, orig_lang_input, target_lang_input, note_sw, req_box,
                    api_key_input, base_url_input, api_path_input, model_input,
                    max_tokens_input, max_completion_tokens_input,
                    reasoning_effort_input, thinking_enabled_input],
            outputs=[result_box, download_file, status_text],
        )
        launch_chat_btn.click(
            fn=launch_chat,
            inputs=[],
            outputs=[chat_status],
        )

    return app


def _build_allowed_paths() -> list[str]:
    """返回 Gradio /file= 路由允许访问的目录列表。

    仅公开当前项目的 output/ 和 doing/ 目录。
    PROJECTS_DIR 不在此列表中,防止跨项目文件访问
    （如 /file=../projects/other-project/history.json）。
    """
    return [str(config.OUTPUT_DIR), str(config.DOING_DIR)]


def main() -> None:
    import argparse

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
    app = build_ui()

    # 允许 Gradio 的 /file= 路由访问输出目录和中间产物目录。
    # 不再包含 PROJECTS_DIR,防止跨项目文件访问。
    allowed_paths = _build_allowed_paths()

    if args.browser:
        app.launch(share=False, inbrowser=True, allowed_paths=allowed_paths, server_name="127.0.0.1")
        return

    # 原生窗口模式:后台启动 Gradio,再用 pywebview 承载页面
    app.launch(prevent_thread_lock=True, allowed_paths=allowed_paths, server_name="127.0.0.1")
    import webview

    # 允许 pywebview 内触发文件下载(如 Gradio 的 File 组件)。
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
        "http://127.0.0.1:7860",
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
