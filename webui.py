"""AI_MIDI Web UI 入口。

基于 Gradio 实现,作为 gui.py 的跨平台替代,可在浏览器中操作。

用法:
    python webui.py
"""
import json
import os
import shutil
import struct
import time
from pathlib import Path

import gradio as gr

import ai_api
import config
import get
import out


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

# ===== 功能常量 =====
FUNC_ADD_CHORD = "配和弦"
FUNC_TRANSLATE = "翻译歌词"
FUNC_MELISMA = "设计转音"
FUNC_OTHER = "其他要求"

_FUNC_CHOICES = [FUNC_ADD_CHORD, FUNC_TRANSLATE, FUNC_MELISMA, FUNC_OTHER]

# 各功能需要哪些字段
_FUNC_FIELDS = {
    FUNC_ADD_CHORD: {"lyrics": False, "lang": False, "note_sw": False, "req": True},
    FUNC_TRANSLATE: {"lyrics": True, "lang": True, "note_sw": False, "req": False},
    FUNC_MELISMA: {"lyrics": True, "lang": False, "note_sw": False, "req": True},
    FUNC_OTHER: {"lyrics": True, "lang": False, "note_sw": True, "req": True},
}

# ===== 用户设置持久化 =====
SETTINGS_FILE: Path = config.PROJECT_ROOT / "settings.json"


def _load_settings() -> dict:
    """从本地 JSON 文件加载用户设置，失败时返回空字典。"""
    if not SETTINGS_FILE.exists():
        return {}
    try:
        return json.loads(SETTINGS_FILE.read_text(encoding="utf-8"))
    except Exception:  # noqa: BLE001
        return {}


def _save_settings(
    api_key: str,
    base_url: str,
    model: str,
    max_tokens: int | float | None,
    max_completion_tokens: int | float | None,
    reasoning_effort: str,
    thinking_enabled: bool,
) -> str:
    """保存用户设置到本地 JSON 文件。"""
    settings = {
        "api_key": api_key,
        "base_url": base_url,
        "model": model,
        "max_tokens": int(max_tokens) if max_tokens else None,
        "max_completion_tokens": int(max_completion_tokens) if max_completion_tokens else None,
        "reasoning_effort": reasoning_effort,
        "thinking_enabled": thinking_enabled,
    }
    try:
        SETTINGS_FILE.write_text(
            json.dumps(settings, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        return "✓ 配置已保存"
    except Exception as e:  # noqa: BLE001
        return f"✗ 保存失败: {e}"


def _note_to_text(note_table: list[str]) -> str:
    """把 note_table 列表合并成字符串。"""
    return "\n".join(note_table)


def _fetch_models(api_key: str, base_url: str) -> tuple[list[str], str]:
    """从 API 服务拉取模型列表,返回 (模型 id 列表, 状态信息)。"""
    key = api_key.strip() or os.environ.get("DEEPSEEK_API_KEY", "")
    url = base_url.strip() or config.BASE_URL

    if not key:
        return [], "⚠ 请先填写 API Key 或设置环境变量 DEEPSEEK_API_KEY"

    try:
        client = ai_api.get_client(api_key=key, base_url=url)
        models = client.models.list()
        ids = sorted([m.id for m in models.data])
        if not ids:
            return [], "⚠ 未获取到任何模型"
        return ids, f"✓ 已获取 {len(ids)} 个模型"
    except Exception as e:  # noqa: BLE001
        return [], f"✗ 获取模型失败: {e}"


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
    except Exception as e:  # noqa: BLE001
        return f"解析失败: {e}", []

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
    except Exception:  # noqa: BLE001
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
    except Exception:  # noqa: BLE001
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
    except Exception:  # noqa: BLE001
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
    except Exception:  # noqa: BLE001
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
    icon_path = _ensure_icon_file(image_path, APP_ICON_FILE)
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
        except Exception:  # noqa: BLE001
            pass
        time.sleep(0.1)

    try:
        import ctypes
    except Exception:  # noqa: BLE001
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
    model: str,
    max_tokens: int | None,
    max_completion_tokens: int | None,
    reasoning_effort: str,
    thinking_enabled: bool,
) -> tuple[str, str | None, str]:
    """调用 AI API 并返回 (结果文本, 可下载文件路径, 状态文本)。"""
    start_time = time.time()

    def _elapsed(msg: str) -> str:
        return f"{msg}（耗时 {time.time() - start_time:.2f} 秒）"

    # 除"其他要求"外,其余功能都需要先解析 MIDI
    if func != FUNC_OTHER and not note_table:
        return "", None, _elapsed("⚠ 请先解析 MIDI 文件。")

    bpm = bpm.strip() or str(config.DEFAULT_BPM)
    time_signature = time_signature.strip() or config.DEFAULT_TIME_SIGNATURE
    note_text = _note_to_text(note_table) if note_table else ""

    # 组装公共 API 参数,空值不传入,让 ai_api 使用默认值
    api_kwargs: dict = {}
    if api_key.strip():
        api_kwargs["api_key"] = api_key.strip()
    if base_url.strip():
        api_kwargs["base_url"] = base_url.strip()
    if model.strip():
        api_kwargs["model"] = model.strip()
    if max_tokens:
        api_kwargs["max_tokens"] = int(max_tokens)
    if max_completion_tokens:
        api_kwargs["max_completion_tokens"] = int(max_completion_tokens)
    if reasoning_effort.strip():
        api_kwargs["reasoning_effort"] = reasoning_effort.strip()
    api_kwargs["thinking_enabled"] = thinking_enabled

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
    except Exception as e:  # noqa: BLE001
        return "", None, _elapsed(f"✗ 调用失败: {e}")

    if not result:
        return "", None, _elapsed("✗ AI 未返回内容或调用失败。")

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

    except Exception as e:  # noqa: BLE001
        return result, None, _elapsed(f"✓ AI 返回结果,但保存失败: {e}")

    return result, download_path, _elapsed(status_msg)


def build_ui() -> gr.Blocks:
    """构建并返回 Gradio 应用。"""
    settings = _load_settings()
    saved_model = settings.get("model", config.MODEL)

    with gr.Blocks(title="AI_MIDI · AI 编曲助手") as app:
        gr.Markdown("# AI_MIDI · AI 编曲助手")
        gr.Markdown("上传 MIDI → 解析 → 选择任务 → AI 处理 → 下载结果")

        # 运行时状态
        note_table_state = gr.State(value=[])

        with gr.Tabs():
            # ==================== 处理标签页 ====================
            with gr.Tab("处理"):
                with gr.Row():
                    with gr.Column(scale=1):
                        # MIDI 上传与解析
                        midi_file = gr.File(
                            label="上传 MIDI 文件",
                            file_types=[".mid", ".midi"],
                        )
                        parse_btn = gr.Button("解析 MIDI", variant="primary")
                        parse_status = gr.Textbox(
                            label="解析状态",
                            value="尚未解析",
                            interactive=False,
                        )

                        # 功能选择
                        func_selector = gr.Radio(
                            label="功能",
                            choices=_FUNC_CHOICES,
                            value=FUNC_ADD_CHORD,
                        )

                        # 公共参数
                        bpm_input = gr.Textbox(
                            label="BPM",
                            value=str(config.DEFAULT_BPM),
                        )
                        timesig_input = gr.Textbox(
                            label="拍号",
                            value=config.DEFAULT_TIME_SIGNATURE,
                        )

                        # 歌词
                        lyrics_box = gr.Textbox(
                            label="歌词",
                            lines=3,
                            visible=False,
                        )

                        # 语言(仅翻译歌词)
                        with gr.Row(visible=False) as lang_row:
                            orig_lang_input = gr.Textbox(
                                label="原语言",
                                placeholder="如:日语",
                            )
                            target_lang_input = gr.Textbox(
                                label="目标语言",
                                placeholder="如:中文",
                            )

                        # 输出音符开关(仅其他要求)
                        note_sw = gr.Checkbox(
                            label="输出音符数据 (MIDI)",
                            value=False,
                            visible=False,
                        )

                        # 具体要求
                        req_box = gr.Textbox(
                            label="具体要求",
                            lines=3,
                            visible=True,
                        )

                        start_btn = gr.Button("▶ 开始", variant="primary")
                        status_text = gr.Textbox(
                            label="状态",
                            interactive=False,
                        )

                    with gr.Column(scale=2):
                        result_box = gr.Textbox(
                            label="结果",
                            lines=20,
                            interactive=False,
                        )
                        download_file = gr.File(label="下载结果")

            # ==================== 设置标签页 ====================
            with gr.Tab("设置"):
                gr.Markdown("## API 与模型参数")

                # API Key 与显隐开关
                with gr.Row():
                    api_key_input = gr.Textbox(
                        label="API Key",
                        type="password",
                        placeholder="sk-...",
                        value=settings.get("api_key", os.environ.get("DEEPSEEK_API_KEY", "")),
                        scale=4,
                    )
                    show_key_sw = gr.Checkbox(
                        label="显示 API Key",
                        value=False,
                        scale=1,
                    )

                base_url_input = gr.Textbox(
                    label="Base URL",
                    value=settings.get("base_url", config.BASE_URL),
                )

                # 模型:下拉选择 + 允许手动输入 + 刷新按钮
                with gr.Row():
                    model_input = gr.Dropdown(
                        label="模型",
                        choices=[saved_model],
                        value=saved_model,
                        allow_custom_value=True,
                        scale=4,
                    )
                    refresh_model_btn = gr.Button("🔄 刷新", scale=1)
                model_status = gr.Textbox(
                    label="模型列表状态",
                    interactive=False,
                    value="点击刷新按钮从 API 获取模型列表",
                )

                gr.Markdown("## 生成参数")
                with gr.Row():
                    max_tokens_input = gr.Number(
                        label="最大上下文 (max_tokens)",
                        value=settings.get("max_tokens"),
                        precision=0,
                        info="留空则使用 API 默认值",
                    )
                    max_completion_tokens_input = gr.Number(
                        label="最大输出长度 (max_completion_tokens)",
                        value=settings.get("max_completion_tokens"),
                        precision=0,
                        info="留空则使用 API 默认值",
                    )
                reasoning_effort_input = gr.Radio(
                    label="推理努力程度 (reasoning_effort)",
                    choices=["low", "medium", "max"],
                    value=settings.get("reasoning_effort", "max"),
                )
                thinking_enabled_input = gr.Checkbox(
                    label="启用 thinking 模式",
                    value=settings.get("thinking_enabled", True),
                )

                save_cfg_btn = gr.Button("💾 保存配置", variant="primary")
                save_cfg_status = gr.Textbox(
                    label="保存状态",
                    interactive=False,
                    value="",
                )

        # 事件绑定
        parse_btn.click(
            fn=_parse_midi,
            inputs=midi_file,
            outputs=[parse_status, note_table_state],
        )

        func_selector.change(
            fn=_update_func_visibility,
            inputs=func_selector,
            outputs=[lyrics_box, lang_row, note_sw, req_box],
        )

        # API Key 显隐切换
        def _toggle_key_visibility(show: bool) -> dict:
            return gr.update(type="text" if show else "password")

        show_key_sw.change(
            fn=_toggle_key_visibility,
            inputs=show_key_sw,
            outputs=api_key_input,
        )

        # 刷新模型列表
        def _refresh_models(api_key: str, base_url: str) -> tuple[dict, str]:
            ids, msg = _fetch_models(api_key, base_url)
            return gr.update(choices=ids), msg

        refresh_model_btn.click(
            fn=_refresh_models,
            inputs=[api_key_input, base_url_input],
            outputs=[model_input, model_status],
        )

        save_cfg_btn.click(
            fn=_save_settings,
            inputs=[
                api_key_input,
                base_url_input,
                model_input,
                max_tokens_input,
                max_completion_tokens_input,
                reasoning_effort_input,
                thinking_enabled_input,
            ],
            outputs=save_cfg_status,
        )

        start_btn.click(
            fn=_run_task,
            inputs=[
                func_selector,
                note_table_state,
                bpm_input,
                timesig_input,
                lyrics_box,
                orig_lang_input,
                target_lang_input,
                note_sw,
                req_box,
                api_key_input,
                base_url_input,
                model_input,
                max_tokens_input,
                max_completion_tokens_input,
                reasoning_effort_input,
                thinking_enabled_input,
            ],
            outputs=[result_box, download_file, status_text],
        )

    return app


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

    if args.browser:
        app.launch(share=False, inbrowser=True)
        return

    # 原生窗口模式:后台启动 Gradio,再用 pywebview 承载页面
    app.launch(prevent_thread_lock=True)
    import webview

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
