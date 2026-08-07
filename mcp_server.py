"""AI_MIDI MCP Server。

通过 FastMCP 暴露 MIDI 操作能力，供 chat_ui.py 的子进程调用。
传输方式：stdio（JSON-RPC over stdin/stdout）。
"""
import json
import os
import shutil
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

# 镜像目录（绑定工作区时为 projects/<id>/midi，用于双写）；未绑定为 None
_env_mirror = os.environ.get("AI_MIDI_MIRROR_DIR", "")
MIRROR_DIR: Path | None = Path(_env_mirror) if _env_mirror else None

MIDI_EXTS = {".mid", ".midi"}


def _safe_join(base_dir: Path, filename: str) -> Path:
    """Resolve filename within base_dir, preventing path traversal.

    Raises ValueError if the resolved path escapes base_dir.
    """
    base_resolved = base_dir.resolve()
    filepath = (base_resolved / filename).resolve()
    if filepath != base_resolved and not filepath.is_relative_to(base_resolved):
        raise ValueError(f"Path traversal detected: {filename}")
    return filepath


def _mirror_path(filepath: Path) -> Path | None:
    """返回 filepath 在镜像目录中的对应路径；无镜像返回 None。"""
    if MIRROR_DIR is None:
        return None
    try:
        rel = filepath.resolve().relative_to(OUTPUT_DIR.resolve())
    except ValueError:
        return None
    return MIRROR_DIR.resolve() / rel


def _write_with_mirror(filepath: Path) -> None:
    """filepath 已写入主目录后，同步复制到镜像目录（若存在）。"""
    mp = _mirror_path(filepath)
    if mp is None:
        return
    try:
        mp.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(filepath, mp)
    except OSError:
        pass


def _cleanup_empty_parents(start: Path) -> None:
    """从 start 向上删除空目录，直到离开 OUTPUT_DIR/MIRROR_DIR 或遇到非空目录。"""
    output_root = OUTPUT_DIR.resolve()
    mirror_root = MIRROR_DIR.resolve() if MIRROR_DIR else None
    cur = start
    while cur.is_dir() and cur != output_root and cur != mirror_root:
        try:
            cur.rmdir()  # 只删空目录
        except OSError:
            break
        cur = cur.parent


def _delete_with_mirror(filepath: Path) -> None:
    """删除主目录文件及镜像副本，并清理空父目录。"""
    targets = [filepath]
    mp = _mirror_path(filepath)
    if mp is not None:
        targets.append(mp)
    for p in targets:
        try:
            if p.exists():
                p.unlink()
        except OSError:
            continue
        _cleanup_empty_parents(p.parent)


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
    """列出当前项目所有 MIDI 文件及其大小（含子目录，路径为相对项目目录）。"""
    if not OUTPUT_DIR.exists():
        return "（output 目录不存在）"
    files = sorted(
        p for p in OUTPUT_DIR.rglob("*")
        if p.is_file() and p.suffix.lower() in MIDI_EXTS
    )
    if not files:
        return "（暂无 MIDI 文件）"
    lines = []
    for f in files:
        rel = f.relative_to(OUTPUT_DIR).as_posix()
        size_kb = max(1, f.stat().st_size // 1024)
        lines.append(f"{rel}  ({size_kb} KB)")
    return "\n".join(lines)


@mcp.tool()
def parse_midi(filename: str) -> str:
    """解析 output 目录下的 MIDI 文件，返回 note_table 格式的音符数据。

    参数：
        filename: MIDI 文件名（如 output.mid、song.mid）

    返回：
         每行一个音符的 note_table 文本，或错误信息。
    """
    try:
        filepath = _safe_join(OUTPUT_DIR, filename)
    except ValueError as e:
        return f"错误：{e}"
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

    try:
        filepath = _safe_join(OUTPUT_DIR, filename)
    except ValueError as e:
        return f"错误：{e}"
    os.makedirs(filepath.parent, exist_ok=True)
    try:
        note_count = len([l for l in note_data.strip().split("\n") if l.strip()])
        out.txt_to_midi(note_data, str(filepath), bpm)
        _write_with_mirror(filepath)
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
    try:
        filepath = _safe_join(OUTPUT_DIR, filename)
    except ValueError as e:
        return f"错误：{e}"
    if not filepath.exists():
        return f"错误：文件不存在 — {filepath}"
    try:
        _delete_with_mirror(filepath)
        return f"成功删除 {filename}"
    except Exception as e:
        return f"错误：删除失败 — {e}"


@mcp.tool()
def create_folder(name: str) -> str:
    """在项目目录下创建文件夹（支持多级子目录）。

    参数：
        name: 文件夹相对路径（如 drums、sectionA/drums）

    返回：
         操作结果说明。
    """
    if not name or not name.strip():
        return "错误：文件夹名不能为空"
    try:
        folder = _safe_join(OUTPUT_DIR, name)
    except ValueError as e:
        return f"错误：{e}"
    try:
        if folder.exists():
            if folder.is_dir():
                return f"文件夹已存在: {name}"
            return f"错误：路径已存在且不是文件夹 - {name}"
        folder.mkdir(parents=True, exist_ok=True)
        mp = _mirror_path(folder)
        if mp is not None:
            try:
                mp.mkdir(parents=True, exist_ok=True)
            except OSError:
                pass
        return f"成功创建文件夹: {name}"
    except Exception as e:
        return f"错误：创建文件夹失败 - {e}"


@mcp.tool()
def list_project_structure() -> str:
    """以树状图列出当前项目所有 MIDI 文件的目录层级结构。

    返回：
         ASCII 树状图，便于直观了解文件组织（文件夹与 .mid/.midi 文件）。
    """
    if not OUTPUT_DIR.exists():
        return "（output 目录不存在）"
    midi_files = sorted(
        p for p in OUTPUT_DIR.rglob("*")
        if p.is_file() and p.suffix.lower() in MIDI_EXTS
    )
    if not midi_files:
        return f"{OUTPUT_DIR.name}/\n（暂无 MIDI 文件）"

    # 构建嵌套树：文件夹->dict，文件->None
    tree: dict = {}
    for p in midi_files:
        parts = p.relative_to(OUTPUT_DIR).parts
        node = tree
        for part in parts[:-1]:
            node = node.setdefault(part, {})
        node[parts[-1]] = None

    lines = [f"{OUTPUT_DIR.name}/"]

    def _render(node: dict, prefix: str) -> None:
        # 文件夹在前（child 非 None），文件在后（child None），各自字母序
        items = sorted(node.items(), key=lambda kv: (kv[1] is None, kv[0]))
        for i, (name, child) in enumerate(items):
            is_last = i == len(items) - 1
            connector = "└── " if is_last else "├── "
            lines.append(f"{prefix}{connector}{name}")
            if child is not None:
                extension = "    " if is_last else "│   "
                _render(child, prefix + extension)

    _render(tree, "")
    return "\n".join(lines)


@mcp.tool()
def read_library_file(filename: str) -> str:
    """读取 Library 目录下的知识文件内容。

    参数：
        filename: 文件名（如 02_配和弦指南.md）

    返回：
         文件内容，或错误信息。
    """
    lib_dir = config.PROJECT_ROOT / "Library"
    try:
        filepath = _safe_join(lib_dir, filename)
    except ValueError as e:
        return f"错误：{e}"
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


def run_mcp_server() -> None:
    """以 stdio 传输运行 MCP 服务器。

    源码环境由 `python mcp_server.py` 进入；PyInstaller 打包版由
    `AI_MIDI.exe --mcp-child` 从 main.py 调用进入（exe 无法执行 .py 脚本）。
    """
    mcp.run(transport="stdio")


if __name__ == "__main__":
    run_mcp_server()
