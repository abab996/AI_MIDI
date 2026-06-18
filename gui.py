"""AI_MIDI 图形界面入口。

基于 maliang(魔改版 tkinter)实现的 Win11 风格 GUI,作为命令行版 main.py 的替代。

功能与 main.py 一致:解析 MIDI → 选择 4 类任务之一 → 后台线程调用 DeepSeek →
展示结果并保存(MIDI 或 txt)。

用法:
    python gui.py
"""
import ctypes
import ctypes.wintypes
import os
import sys
import threading
import tkinter as tk
from pathlib import Path
from tkinter import filedialog, messagebox

import maliang
from maliang import Tk, Canvas
from maliang import Text, Label, Button, InputBox, SegmentedButton, Switch, Spinner
from maliang.theme import manager as theme_manager

import config
import get
import out
import ai_api


def _setup_safe_scale() -> float:
    """启用 Windows DPI 感知并返回适合当前屏幕的缩放比例。"""
    if sys.platform != "win32":
        return 1.0
    try:
        # Windows 8.1+: 每个显示器独立 DPI 感知
        ctypes.windll.shcore.SetProcessDpiAwareness(2)
    except Exception:
        try:
            # Windows Vista/7/8 回退
            ctypes.windll.user32.SetProcessDPIAware()
        except Exception:
            pass

    raw_scale = 1.0
    try:
        hdc = ctypes.windll.user32.GetDC(0)
        dpi = ctypes.windll.gdi32.GetDeviceCaps(hdc, 88)  # LOGPIXELSX
        ctypes.windll.user32.ReleaseDC(0, hdc)
        raw_scale = dpi / 96.0
    except Exception:
        pass

    # 根据主显示器工作区限制最大缩放,避免窗口超出屏幕
    try:
        user32 = ctypes.windll.user32
        rect = ctypes.wintypes.RECT()
        if user32.SystemParametersInfoW(48, 0, ctypes.byref(rect), 0):
            work_w = rect.right - rect.left
            work_h = rect.bottom - rect.top
            # 预留 10% 边距,基于原始设计尺寸 740x780
            max_scale = min(
                (work_w * 0.9) / 740,
                (work_h * 0.9) / 780,
            )
            # 允许在小屏幕上适当缩小,但不宜过小影响可读性
            return min(raw_scale, max(0.75, max_scale))
    except Exception:
        pass

    return raw_scale


_SCALE = _setup_safe_scale()


def _s(value: int) -> int:
    """按 DPI 缩放比例缩放整数值。"""
    return int(round(value * _SCALE))


# ===== 布局常量(已按系统 DPI 自动缩放) =====
PAD = _s(20)                     # 外边距
WIN_W, WIN_H = _s(740), _s(780)  # 窗口尺寸
ROW_H = _s(34)                   # 单行高度(输入框 / 按钮)
GAP = _s(10)                     # 行间距
MULTI_H = _s(72)                 # 多行文本框高度

# 功能标识(与 SegmentedButton 索引对应)
FUNC_ADD_CHORD = 0
FUNC_TRANSLATE = 1
FUNC_MELISMA = 2
FUNC_OTHER = 3

# 各功能需要哪些行: lyrics=歌词, lang=语言, note_sw=输出开关, req=要求
_FUNC_ROWS = {
    FUNC_ADD_CHORD: {"lyrics": False, "lang": False, "note_sw": False, "req": True},
    FUNC_TRANSLATE: {"lyrics": True,  "lang": True,  "note_sw": False, "req": False},
    FUNC_MELISMA:   {"lyrics": True,  "lang": False, "note_sw": False, "req": True},
    FUNC_OTHER:     {"lyrics": True,  "lang": False, "note_sw": True,  "req": True},
}


class App:
    """主应用类,封装全部 GUI 状态与逻辑。"""

    def __init__(self) -> None:
        self.root = Tk((WIN_W, WIN_H), title="AI_MIDI · AI 编曲助手")
        self.root.center()
        self.root.at_exit(self._on_close)

        self.canvas = Canvas(self.root, expand="xy", keep_ratio=None,
                             auto_zoom=False, auto_update=True)
        self.canvas.place(x=0, y=0, width=WIN_W, height=WIN_H)

        # 运行时状态
        self.current_func: int = FUNC_ADD_CHORD
        self.note_table: list[str] = []
        self.midi_path: str = str(config.INPUT_MIDI)
        self.is_running: bool = False
        self._result_text: str = ""

        # 跟随主题的 tkinter.Text 控件列表
        self._tk_texts: list[tuple[tk.Text, tuple]] = []   # (widget, geometry)
        self._theme_handler = lambda mode: self._sync_text_theme()

        self._build_ui()

    # ================================================================== #
    #  UI 构建 —— 所有控件在固定坐标创建,切换功能时只 forget / 显示       #
    # ================================================================== #
    def _build_ui(self) -> None:
        y = PAD

        # —— 标题 ——
        Text(self.canvas, (PAD, y), text="AI_MIDI", fontsize=_s(22), weight="bold")
        y += _s(36)
        Text(self.canvas, (PAD, y),
             text="解析 MIDI → 选择任务 → DeepSeek AI 处理 → 输出结果",
             fontsize=_s(10))
        y += _s(30)

        # —— 分隔线(用 Label 模拟) ——
        Label(self.canvas, (PAD, y), (WIN_W - PAD * 2, 1))
        y += _s(8)

        # —— MIDI 文件选择 ——
        Text(self.canvas, (PAD, y + _s(8)), text="输入 MIDI", fontsize=_s(11))
        self.midi_path_input = InputBox(
            self.canvas, (PAD + _s(82), y),
            (WIN_W - _s(82) - _s(120) - PAD * 2, ROW_H),
            placeholder="选择 .mid 文件…", fontsize=_s(10))
        self.midi_path_input.set(self.midi_path)

        self.pick_btn = Button(self.canvas, (WIN_W - _s(120) - PAD, y),
                               (_s(120), ROW_H), text="浏览…", fontsize=_s(10),
                               command=self._pick_midi)
        y += ROW_H + GAP

        # —— 解析按钮 + 状态 ——
        self.parse_btn = Button(self.canvas, (PAD, y), (_s(120), ROW_H),
                                text="解析 MIDI", fontsize=_s(10),
                                command=self._parse_midi)
        self.parse_status = Text(self.canvas, (PAD + _s(132), y + _s(8)),
                                 text="尚未解析", fontsize=_s(10))
        y += ROW_H + GAP + _s(6)

        # —— 分隔线 ——
        Label(self.canvas, (PAD, y), (WIN_W - PAD * 2, 1))
        y += _s(8)

        # —— 功能选择 ——
        Text(self.canvas, (PAD, y + _s(6)), text="功能", fontsize=_s(11))
        self.func_selector = SegmentedButton(
            self.canvas, (PAD + _s(60), y),
            text=("配和弦", "翻译歌词", "设计转音", "其他要求"),
            fontsize=_s(10), default=FUNC_ADD_CHORD,
            command=self._on_func_change)
        y += _s(48)

        # —— 公共参数: BPM + 拍号(始终可见) ——
        Text(self.canvas, (PAD, y + _s(8)), text="BPM", fontsize=_s(11))
        self.bpm_input = InputBox(
            self.canvas, (PAD + _s(60), y), (_s(100), ROW_H),
            placeholder="120", fontsize=_s(10))
        self.bpm_input.set(str(config.DEFAULT_BPM))

        Text(self.canvas, (PAD + _s(185), y + _s(8)), text="拍号", fontsize=_s(11))
        self.timesig_input = InputBox(
            self.canvas, (PAD + _s(230), y), (_s(100), ROW_H),
            placeholder="4/4", fontsize=_s(10))
        self.timesig_input.set(config.DEFAULT_TIME_SIGNATURE)
        y += ROW_H + GAP

        # —— 歌词(多行) ——
        self.lyrics_label = Text(self.canvas, (PAD, y), text="歌词", fontsize=_s(11))
        y += _s(20)
        geo_lyrics = (PAD, y, WIN_W - PAD * 2, MULTI_H)
        self.lyrics_box = self._make_text(*geo_lyrics)
        self._lyrics_geo = geo_lyrics
        y += MULTI_H + GAP

        # —— 原语言 / 目标语言(仅翻译歌词) ——
        self.orig_lang_label = Text(self.canvas, (PAD, y + _s(8)),
                                    text="原语言", fontsize=_s(11))
        self.orig_lang_input = InputBox(
            self.canvas, (PAD + _s(80), y), (_s(180), ROW_H),
            placeholder="如:日语", fontsize=_s(10))

        self.target_lang_label = Text(self.canvas, (PAD + _s(280), y + _s(8)),
                                      text="目标语言", fontsize=_s(11))
        self.target_lang_input = InputBox(
            self.canvas, (PAD + _s(356), y), (_s(180), ROW_H),
            placeholder="如:中文", fontsize=_s(10))
        y += ROW_H + GAP

        # —— 输出音符开关(仅其他要求) ——
        self.note_sw_label = Text(self.canvas, (PAD, y + _s(6)),
                                  text="输出音符数据 (MIDI)", fontsize=_s(11))
        self.note_sw = Switch(self.canvas, (PAD + _s(210), y), length=_s(48), default=False)
        y += ROW_H + GAP

        # —— 具体要求(多行) ——
        self.req_label = Text(self.canvas, (PAD, y), text="具体要求", fontsize=_s(11))
        y += _s(20)
        geo_req = (PAD, y, WIN_W - PAD * 2, MULTI_H)
        self.req_box = self._make_text(*geo_req)
        self._req_geo = geo_req
        y += MULTI_H + GAP + _s(4)

        # —— 分隔线 ——
        Label(self.canvas, (PAD, y), (WIN_W - PAD * 2, 1))
        y += _s(8)

        # —— 开始按钮 + 加载动画 + 状态 ——
        self.start_btn = Button(self.canvas, (PAD, y), (_s(150), _s(38)),
                                text="▶  开始", fontsize=_s(12), weight="bold",
                                command=self._start_task)
        self.spinner = Spinner(self.canvas, (PAD + _s(168), y + _s(3)), (_s(32), _s(32)),
                               mode="indeterminate")
        self.spinner.forget()
        self.status_text = Text(self.canvas, (PAD + _s(218), y + _s(10)),
                                text="", fontsize=_s(10))
        y += _s(52)

        # —— 结果 ——
        Text(self.canvas, (PAD, y), text="结果", fontsize=_s(11), weight="bold")
        y += _s(20)
        result_h = WIN_H - y - PAD - _s(8)
        geo_result = (PAD, y, WIN_W - PAD * 2, result_h)
        self.result_box = self._make_text(*geo_result)
        self.result_box.insert("1.0", "(结果将显示在此处)")
        self.result_box.config(state="disabled")

        # —— 保存按钮(右下) ——
        self.save_btn = Button(
            self.canvas, (WIN_W - _s(130) - PAD, WIN_H - _s(36)),
            (_s(130), _s(28)), text="另存为…", fontsize=_s(10),
            command=self._save_result)
        self.save_btn.disable()

        # 初始显隐
        self._apply_func_visibility()

    def _make_text(self, x: int, y: int, w: int, h: int) -> tk.Text:
        """创建一个跟随主题的多行 tkinter.Text。"""
        t = tk.Text(self.canvas, width=1, height=1, wrap="word",
                    relief="flat", bd=0, padx=_s(8), pady=_s(6),
                    highlightthickness=1, highlightbackground="#CCCCCC",
                    font=("Microsoft YaHei UI", _s(10)))
        t.place(x=x, y=y, width=w, height=h)
        self._tk_texts.append((t, (x, y, w, h)))
        self._sync_text_theme()
        return t

    # ================================================================== #
    #  动态显隐                                                          #
    # ================================================================== #
    def _on_func_change(self, index: int) -> None:
        self.current_func = index
        self._apply_func_visibility()

    def _apply_func_visibility(self) -> None:
        """根据当前功能显示/隐藏对应参数行。位置固定不变,隐藏的行留白。"""
        rows = _FUNC_ROWS[self.current_func]

        def _vis(widget, show: bool):
            widget.forget(not show)
            # maliang 的 forget 偶尔无法隐藏 placeholder 等 canvas item,
            # 强制设置 item state 确保彻底显隐
            state = "normal" if show else "hidden"
            master = getattr(widget, "master", None)
            if master is None:
                return
            for element in getattr(widget, "elements", ()):
                for item in getattr(element, "items", ()):
                    try:
                        master.itemconfigure(item, state=state)
                    except Exception:  # noqa: BLE001
                        pass

        def _vis_tk(idx: int, show: bool):
            """idx: 0=lyrics, 1=req, 2=result"""
            if idx >= len(self._tk_texts):
                return
            t, geo = self._tk_texts[idx]
            if show:
                if not t.winfo_ismapped():
                    t.place(x=geo[0], y=geo[1], width=geo[2], height=geo[3])
            else:
                t.place_forget()

        # 歌词区(索引 0)
        _vis(self.lyrics_label, rows["lyrics"])
        _vis_tk(0, rows["lyrics"])

        # 语言行
        _vis(self.orig_lang_label, rows["lang"])
        _vis(self.orig_lang_input, rows["lang"])
        _vis(self.target_lang_label, rows["lang"])
        _vis(self.target_lang_input, rows["lang"])

        # 输出开关
        _vis(self.note_sw_label, rows["note_sw"])
        _vis(self.note_sw, rows["note_sw"])

        # 要求区(索引 1)
        _vis(self.req_label, rows["req"])
        _vis_tk(1, rows["req"])

    def _sync_text_theme(self) -> None:
        """同步所有 tkinter.Text 的配色到当前主题。"""
        mode = theme_manager.get_color_mode()
        if mode == "dark":
            bg, fg, ins, hl = "#2B2B2B", "#FFFFFF", "#FFFFFF", "#3D3D3D"
        else:
            bg, fg, ins, hl = "#FFFFFF", "#000000", "#000000", "#CCCCCC"
        for t, _ in self._tk_texts:
            try:
                t.config(bg=bg, fg=fg, insertbackground=ins,
                         highlightbackground=hl, selectbackground=hl)
            except tk.TclError:
                pass

    # ================================================================== #
    #  事件处理                                                          #
    # ================================================================== #
    def _pick_midi(self) -> None:
        path = filedialog.askopenfilename(
            title="选择 MIDI 文件",
            filetypes=[("MIDI 文件", "*.mid *.midi"), ("所有文件", "*.*")],
            initialdir=str(config.INPUT_MIDI.parent))
        if path:
            self.midi_path = path
            self.midi_path_input.set(path)
            self.parse_status.set("已更换文件,请重新解析")

    def _parse_midi(self) -> None:
        path = self.midi_path_input.get().strip()
        if not path or not os.path.isfile(path):
            messagebox.showwarning("提示", "请先选择有效的 MIDI 文件。")
            return
        try:
            self.note_table = get.get_note(path, save_to_file=False)
        except Exception as e:  # noqa: BLE001
            messagebox.showerror("解析失败", f"读取 MIDI 时出错:\n{e}")
            self.note_table = []
        if self.note_table:
            self.parse_status.set(f"✓ 已解析 {len(self.note_table)} 个音符")
        else:
            self.parse_status.set("✗ 未解析出音符")

    def _start_task(self) -> None:
        if self.is_running:
            return
        if not self.note_table:
            messagebox.showwarning("提示", "请先点击「解析 MIDI」获取音符数据。")
            return
        params = self._collect_params()
        if params is None:
            return
        self._set_running(True)
        threading.Thread(target=self._run_task, args=(params,), daemon=True).start()

    def _collect_params(self) -> dict | None:
        bpm = self.bpm_input.get().strip() or str(config.DEFAULT_BPM)
        timesig = self.timesig_input.get().strip() or config.DEFAULT_TIME_SIGNATURE
        lyrics = self.lyrics_box.get("1.0", "end-1c")
        req = self.req_box.get("1.0", "end-1c")
        note_str = "\n".join(self.note_table)

        f = self.current_func
        if f == FUNC_ADD_CHORD:
            if not req.strip():
                messagebox.showwarning("提示", "请填写配和弦的具体要求。")
                return None
            return {"func": f, "note_table": note_str, "bpm": bpm,
                    "time_signature": timesig, "requirements": req}

        if f == FUNC_TRANSLATE:
            orig = self.orig_lang_input.get().strip()
            target = self.target_lang_input.get().strip()
            if not orig or not target:
                messagebox.showwarning("提示", "请填写原语言和目标语言。")
                return None
            return {"func": f, "note_table": note_str, "lyrics": lyrics,
                    "bpm": bpm, "time_signature": timesig,
                    "original_language": orig, "target_language": target}

        if f == FUNC_MELISMA:
            return {"func": f, "note_table": note_str, "lyrics": lyrics,
                    "bpm": bpm, "time_signature": timesig, "requirements": req}

        # FUNC_OTHER
        note_output = self.note_sw.get()
        return {"func": f, "note_table": note_str, "lyrics": lyrics,
                "bpm": bpm, "time_signature": timesig,
                "requirements": req, "note_output": note_output}

    # ================================================================== #
    #  后台任务                                                          #
    # ================================================================== #
    def _run_task(self, params: dict) -> None:
        """后台线程:调用 ai_api,完成后回到主线程更新 UI。"""
        try:
            func_id = params.pop("func")
            if func_id == FUNC_ADD_CHORD:
                result = ai_api.add_chord(**params)
            elif func_id == FUNC_TRANSLATE:
                result = ai_api.translate_lyrics(**params)
            elif func_id == FUNC_MELISMA:
                result = ai_api.design_melisma(**params)
            else:
                result = ai_api.other_requirements(**params)
            self.root.after(0, lambda: self._on_done(result, params, func_id))
        except Exception as e:  # noqa: BLE001
            self.root.after(0, lambda: self._on_error(str(e)))

    def _on_done(self, result: str, params: dict, func_id: int) -> None:
        self._set_running(False)
        self._result_text = result or ""
        self._show_result(self._result_text or "(AI 未返回内容)")

        if not result:
            self._set_status("完成,但 AI 未返回内容")
            return

        try:
            bpm = params.get("bpm", config.DEFAULT_BPM)
            if func_id == FUNC_TRANSLATE:
                self._auto_save_txt("translated_lyrics.txt", result)
                self._set_status("✓ 歌词已保存")
            elif func_id == FUNC_OTHER and not params.get("note_output", False):
                self._auto_save_txt("other_requirements_result.txt", result)
                self._set_status("✓ 结果已保存")
            else:
                out.out_note(result, bpm)
                self._set_status(f"✓ MIDI 已生成: {config.OUTPUT_MIDI.name}")
            self.save_btn.enable()
        except Exception as e:  # noqa: BLE001
            self._set_status(f"保存失败: {e}")

    def _on_error(self, err: str) -> None:
        self._set_running(False)
        self._show_result(f"调用失败:\n{err}")
        self._set_status("✗ 出错")

    # ================================================================== #
    #  运行态 & 工具方法                                                 #
    # ================================================================== #
    def _set_running(self, running: bool) -> None:
        self.is_running = running
        if running:
            self.start_btn.disable()
            self.parse_btn.disable()
            self.pick_btn.disable()
            self.spinner.forget(False)
            self._set_status("AI 处理中…")
        else:
            self.spinner.forget(True)
            self.start_btn.enable()
            self.parse_btn.enable()
            self.pick_btn.enable()

    def _set_status(self, msg: str) -> None:
        self.status_text.set(msg)

    def _show_result(self, text: str) -> None:
        self.result_box.config(state="normal")
        self.result_box.delete("1.0", "end")
        self.result_box.insert("1.0", text)
        self.result_box.config(state="disabled")

    def _save_result(self) -> None:
        if not self._result_text:
            return
        path = filedialog.asksaveasfilename(
            title="另存为",
            defaultextension=".txt",
            filetypes=[("文本文件", "*.txt"), ("MIDI 文件", "*.mid"),
                       ("所有文件", "*.*")],
            initialdir=str(config.OUTPUT_DIR))
        if path:
            Path(path).write_text(self._result_text, encoding="utf-8")

    def _auto_save_txt(self, filename: str, content: str) -> None:
        os.makedirs(config.OUTPUT_DIR, exist_ok=True)
        (config.OUTPUT_DIR / filename).write_text(content, encoding="utf-8")

    def _on_close(self) -> None:
        theme_manager.remove_event(self._theme_handler)
        try:
            self.root.quit()
        except Exception:  # noqa: BLE001
            pass

    def run(self) -> None:
        theme_manager.register_event(self._theme_handler)
        self.root.mainloop()


def main() -> None:
    App().run()


if __name__ == "__main__":
    main()
