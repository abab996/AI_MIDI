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

# MIDI 文件所在的输出目录（优先从环境变量读取，支持项目级目录）
_env_output = os.environ.get("AI_MIDI_OUTPUT_DIR", "")
OUTPUT_DIR: Path = Path(_env_output) if _env_output else config.OUTPUT_DIR


def _normalize_note_data(note_data: str) -> str:
    """将 AI 输出的各种格式转换为 note_table 文本格式。

    支持：
    - JSON 数组格式: [{"note": "C4", "velocity": 80, "start": 1, "end": 2}, ...]
    - JSON 数组字符串: '[{"note": "C4", ...}, ...]'
    - note_table 文本格式（原样返回）
    """
    stripped = note_data.strip()

    # 尝试 JSON 解析
    if stripped.startswith("["):
        try:
            parsed = json.loads(stripped)
            if isinstance(parsed, list) and parsed:
                # 检查第一个元素是否是 note 对象
                first = parsed[0]
                if isinstance(first, dict) and "note" in first:
                    lines = []
                    for n in parsed:
                        note_name = n.get("note", "")
                        vel = n.get("velocity", 80)
                        start = n.get("start", 0)
                        end = n.get("end", 0)
                        lines.append(
                            f'[note: "{note_name}", velocity: "{vel}", '
                            f'start: "{start}", end: "{end}"]'
                        )
                    return "\n".join(lines)
        except json.JSONDecodeError:
            pass

    # 原样返回（已是 note_table 文本格式）
    return note_data


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
def create_midi(filename: str = "output.mid", bpm: int = 120, notes: str = "", note_table: str = "") -> str:
    """从 note_table 数据创建 MIDI 文件，保存到 output 目录。

    参数：
        filename: 输出文件名（如 melody.mid），默认 output.mid
        bpm: 速度（如 120），默认 120
        notes: note_table 格式的音符数据，每行一个音符。
               格式示例：
               [note: "C4", velocity: "80", start: "1", end: "2"]
               [note: "E4", velocity: "60", start: "1", end: "1.5"]

    返回：
        操作结果说明。
    """
    # 兼容：AI 可能用 note_table 或 notes 两种参数名
    note_data = notes or note_table
    if not note_data:
        return "错误：缺少 notes 参数，请提供 note_table 格式的音符数据"

    # 兼容：AI 可能传 JSON 数组格式，需要转换为 note_table 文本格式
    note_data = _normalize_note_data(note_data)

    filepath = OUTPUT_DIR / filename
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    try:
        note_count = len([l for l in note_data.strip().split("\n") if l.strip()])
        out.txt_to_midi(note_data, str(filepath), bpm)
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
