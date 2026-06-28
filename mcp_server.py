"""AI_MIDI MCP Server。

通过 FastMCP 暴露 MIDI 操作能力，供 chat_ui.py 的子进程调用。
传输方式：stdio（JSON-RPC over stdin/stdout）。
"""
from __future__ import annotations

import json
import os
import sys
from pathlib import Path

from mcp.server.fastmcp import FastMCP

import config
import get
import out

# ===== 初始化 MCP Server =====
mcp = FastMCP("ai-midi-tools")

# MIDI 文件所在的输出目录
OUTPUT_DIR: Path = config.OUTPUT_DIR


# ===== Tools =====

@mcp.tool()
def list_midi_files() -> str:
    """列出 output 目录下所有 MIDI 文件及其大小。"""
    if not OUTPUT_DIR.exists():
        return "（output 目录不存在）"
    files = sorted(OUTPUT_DIR.glob("*.mid"))
    if not files:
        return "（暂无 MIDI 文件）"
    lines = []
    for f in files:
        size_kb = max(1, f.stat().st_size // 1024)
        lines.append(f"{f.name}  ({size_kb} KB)")
    return "\n".join(lines)


@mcp.tool()
def parse_midi(filename: str) -> str:
    """解析 output 目录下的 MIDI 文件，返回 note_table 格式的音符数据。

    参数：
        filename: MIDI 文件名（如 output.mid、song.mid）

    返回：
        每行一个音符的 note_table 文本，或错误信息。
    """
    filepath = OUTPUT_DIR / filename
    if not filepath.exists():
        return f"错误：文件不存在 — {filepath}"
    try:
        note_table = get.get_note(str(filepath), save_to_file=False)
        if not note_table:
            return "错误：未能从文件中解析出任何音符。"
        return "\n".join(note_table)
    except Exception as e:
        return f"错误：解析失败 — {e}"


@mcp.tool()
def create_midi(filename: str, bpm: int, notes: str) -> str:
    """从 note_table 数据创建 MIDI 文件，保存到 output 目录。

    参数：
        filename: 输出文件名（如 melody.mid）
        bpm: 速度（如 120）
        notes: note_table 格式的音符数据，每行一个音符。
               格式示例：
               [note: "C4", velocity: "80", start: "1", end: "2"]
               [note: "E4", velocity: "60", start: "1", end: "1.5"]

    返回：
        操作结果说明。
    """
    filepath = OUTPUT_DIR / filename
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    try:
        note_count = len([l for l in notes.strip().split("\n") if l.strip()])
        out.txt_to_midi(notes, str(filepath), bpm)
        size_kb = max(1, filepath.stat().st_size // 1024)
        return f"成功创建 {filename}：{note_count} 个音符，BPM {bpm}，{size_kb} KB"
    except Exception as e:
        return f"错误：创建失败 — {e}"


@mcp.tool()
def delete_midi(filename: str) -> str:
    """删除 output 目录下的 MIDI 文件。

    参数：
        filename: 要删除的 MIDI 文件名（如 old.mid）

    返回：
        操作结果说明。
    """
    filepath = OUTPUT_DIR / filename
    if not filepath.exists():
        return f"错误：文件不存在 — {filepath}"
    try:
        filepath.unlink()
        return f"成功删除 {filename}"
    except Exception as e:
        return f"错误：删除失败 — {e}"


@mcp.tool()
def read_library_file(filename: str) -> str:
    """读取 Library 目录下的知识文件内容。

    参数：
        filename: 文件名（如 02_配和弦指南.md）

    返回：
        文件内容，或错误信息。
    """
    lib_dir = config.PROJECT_ROOT / "Library"
    filepath = lib_dir / filename
    if not filepath.exists():
        available = sorted(p.name for p in lib_dir.glob("*.md")) if lib_dir.exists() else []
        return (
            f"错误：文件不存在 — {filename}\n"
            f"可用文件：{', '.join(available)}"
        )
    try:
        return filepath.read_text(encoding="utf-8")
    except Exception as e:
        return f"错误：读取失败 — {e}"


if __name__ == "__main__":
    mcp.run(transport="stdio")
