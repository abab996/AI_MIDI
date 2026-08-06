"""AI_MIDI 兼容启动器（旧命令入口）。

历史：run.py 原为 Gradio 时代的独立启动器（引用已删除的 webui 模块，
带无边框启动图 Go.png + tkinter 消息框）；Gradio 重构为 FastAPI 后
唯一活跃入口是 main.py。本文件仅保留旧启动命令的兼容性：
参数原样透传给 main.py（--browser 用系统浏览器打开 / 默认 pywebview
原生窗口 / --scale 调整窗口占屏幕比例）。

    python run.py             # 等效 python main.py
    python run.py --browser   # 等效 python main.py --browser
"""

from __future__ import annotations

import sys

# Windows 下 stdout 可能为 GBK，提前切 UTF-8 避免 emoji/中文打印崩溃
if sys.platform == "win32":
    for _stream in (sys.stdout, sys.stderr):
        if _stream is not None and hasattr(_stream, "reconfigure"):
            _stream.reconfigure(encoding="utf-8")

from main import main  # noqa: E402  (先切编码再导入)

if __name__ == "__main__":
    raise SystemExit(main())
