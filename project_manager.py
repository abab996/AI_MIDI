"""项目管理模块。

管理多轮对话项目的创建、删除、重命名、复制、搜索和持久化。
所有项目存储在 projects/ 目录下，每个项目一个 UUID 子目录。
"""
import json
import logging
import os
import shutil
import uuid
from datetime import datetime
from pathlib import Path

import config

logger = logging.getLogger("ai_midi")

# ===== 路径 =====
PROJECTS_DIR: Path = config.PROJECT_ROOT / "projects"
INDEX_FILE: Path = PROJECTS_DIR / "index.json"


def _ensure_dir() -> None:
    """确保 projects 目录存在。"""
    PROJECTS_DIR.mkdir(parents=True, exist_ok=True)


def _now_str() -> str:
    """返回当前时间的 ISO 格式字符串（不含微秒）。"""
    return datetime.now().strftime("%Y-%m-%dT%H:%M:%S")


# ==================== Index 索引管理 ====================

def _load_index() -> list[dict]:
    """加载 projects/index.json；文件不存在或损坏时返回空列表。"""
    if not INDEX_FILE.exists():
        return []
    try:
        data = json.loads(INDEX_FILE.read_text(encoding="utf-8"))
        return data.get("projects", [])
    except (OSError, json.JSONDecodeError):
        logger.exception("读取项目索引失败")
        return []


def _save_index(index: list[dict]) -> None:
    """原子写入 projects/index.json。"""
    _ensure_dir()
    tmp = INDEX_FILE.with_suffix(".tmp")
    try:
        tmp.write_text(
            json.dumps({"projects": index}, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        tmp.replace(INDEX_FILE)
    except OSError:
        logger.exception("写入项目索引失败")
        if tmp.exists():
            tmp.unlink()
        raise


def _update_index_entry(project_id: str, **fields) -> None:
    """更新索引中单个项目的字段。"""
    index = _load_index()
    for entry in index:
        if entry["id"] == project_id:
            entry.update(fields)
            break
    _save_index(index)


def _remove_index_entry(project_id: str) -> None:
    """从索引中移除一个项目。"""
    index = _load_index()
    index = [e for e in index if e["id"] != project_id]
    _save_index(index)


# ==================== 路径工具 ====================

def project_dir(project_id: str) -> Path:
    """返回项目根目录路径。"""
    return PROJECTS_DIR / project_id


def midi_dir(project_id: str) -> Path:
    """返回项目的 midi/ 子目录路径。"""
    return PROJECTS_DIR / project_id / "midi"


def resolve_midi_path(project_id: str, filename: str) -> Path:
    """返回项目 midi 目录中指定文件的绝对路径。

    对 filename 执行路径穿越防护：
    - 拒绝空文件名
    - 拒绝含空字节的文件名
    - 拒绝绝对路径（包括 Windows 盘符路径）
    - 拒绝含 ".." 或 "~" 的文件名
    - 拒绝解析后逃逸出项目 midi/ 目录的路径

    Raises:
        ValueError: 文件名不合法或路径逃逸。
    """
    if not filename:
        raise ValueError("filename 不能为空")
    if "\x00" in filename:
        raise ValueError("filename 包含非法字符 (空字节)")
    if ".." in filename:
        raise ValueError(f"filename 包含非法路径段: {filename}")
    if "~" in filename:
        raise ValueError(f"filename 包含非法字符 '~': {filename}")
    if os.path.isabs(filename):
        raise ValueError(f"filename 不能是绝对路径: {filename}")

    base_dir = get_midi_base_dir(project_id)
    filepath = (base_dir / filename).resolve()

    if filepath != base_dir and not filepath.is_relative_to(base_dir):
        raise ValueError(f"路径逃逸检测: {filename}")

    return filepath


# ==================== 工作区绑定 ====================

MIDI_EXTS = {".mid", ".midi"}


def get_workspace_dir(project_id: str) -> str | None:
    """返回项目绑定的工作区目录绝对路径；未绑定返回 None。"""
    meta = load_project(project_id)
    ws = meta.get("workspace_dir")
    if ws and str(ws).strip():
        return str(ws).strip()
    return None


def set_workspace_dir(project_id: str, path: str | None) -> None:
    """设置/清除项目的工作区绑定，写入 meta.json 并刷新索引时间。"""
    pdir = project_dir(project_id)
    meta_file = pdir / "meta.json"
    meta: dict = {}
    if meta_file.exists():
        try:
            meta = json.loads(meta_file.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            meta = {}
    if path:
        meta["workspace_dir"] = str(path).strip()
    else:
        meta.pop("workspace_dir", None)
    meta["updated_at"] = _now_str()
    meta_file.write_text(
        json.dumps(meta, ensure_ascii=False, indent=2), encoding="utf-8",
    )
    _update_index_entry(project_id, updated_at=meta["updated_at"])


def get_midi_base_dir(project_id: str) -> Path:
    """返回当前主 MIDI 目录：绑定->工作区，否则 projects/<id>/midi。"""
    ws = get_workspace_dir(project_id)
    if ws:
        return Path(ws).expanduser().resolve()
    return midi_dir(project_id).resolve()


def get_midi_mirror_dir(project_id: str) -> Path | None:
    """返回镜像目录：绑定时为 projects/<id>/midi（用于双写），否则 None。"""
    if get_workspace_dir(project_id):
        return midi_dir(project_id).resolve()
    return None


def scan_midi_files(base_dir: Path) -> list[dict]:
    """递归扫描 base_dir 下的 mid 文件，返回 [{name, path, size}]。

    name 为相对 base_dir 的 posix 路径（含子目录），path 为绝对路径。
    非 mid 文件自动过滤。
    """
    base = Path(base_dir)
    if not base.exists():
        return []
    result: list[dict] = []
    for p in sorted(base.rglob("*")):
        if p.is_file() and p.suffix.lower() in MIDI_EXTS:
            try:
                rel = p.relative_to(base).as_posix()
            except ValueError:
                continue
            try:
                size = p.stat().st_size
            except OSError:
                size = 0
            result.append({"name": rel, "path": str(p.resolve()), "size": size})
    return result


def _merge_note_table(scanned: list[dict], prev: list[dict]) -> list[dict]:
    """把 prev 中的 note_table 按 name(relpath) 合并到 scanned 结果。"""
    prev_nt = {f.get("name"): f.get("note_table", "") for f in prev}
    for f in scanned:
        f["note_table"] = prev_nt.get(f["name"], "")
    return scanned


def bind_workspace(project_id: str, workspace_dir: str) -> dict:
    """绑定工作区目录并初始化同步 projects/<id>/midi -> 工作区。

    - 校验/创建 workspace_dir
    - 把 projects/<id>/midi 的 mid 文件复制到工作区（目标已存在则加序号 stem_2.mid）
    - 返回 {"renamed": [(orig_rel, new_rel)], "midi_files": [...]}
    """
    ws = Path(workspace_dir).expanduser().resolve()
    if ws.exists() and not ws.is_dir():
        raise ValueError(f"路径已存在且不是目录: {ws}")
    ws.mkdir(parents=True, exist_ok=True)

    set_workspace_dir(project_id, str(ws))

    src_dir = midi_dir(project_id).resolve()
    renamed: list[tuple[str, str]] = []
    if src_dir.exists():
        for src_file in sorted(src_dir.rglob("*")):
            if not (src_file.is_file() and src_file.suffix.lower() in MIDI_EXTS):
                continue
            rel = src_file.relative_to(src_dir).as_posix()
            dst = ws / rel
            if dst.exists():
                stem, suffix = dst.stem, dst.suffix
                i = 2
                while True:
                    cand = dst.with_name(f"{stem}_{i}{suffix}")
                    if not cand.exists():
                        dst = cand
                        break
                    i += 1
                renamed.append((rel, dst.relative_to(ws).as_posix()))
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src_file, dst)

    _, prev_midi_files = load_history(project_id)
    midi_files = _merge_note_table(scan_midi_files(ws), prev_midi_files)
    logger.info("项目 %s 已绑定工作区: %s（重名 %d 个）", project_id, ws, len(renamed))
    return {"renamed": renamed, "midi_files": midi_files}


def unbind_workspace(project_id: str) -> list[dict]:
    """解绑工作区（不动工作区文件），返回 projects/<id>/midi 的 midi_files。"""
    set_workspace_dir(project_id, None)
    _, prev_midi_files = load_history(project_id)
    base = midi_dir(project_id).resolve()
    logger.info("项目 %s 已解绑工作区", project_id)
    return _merge_note_table(scan_midi_files(base), prev_midi_files)


def sync_workspace_to_projects(project_id: str, prev_midi_files: list[dict]) -> list[dict]:
    """以工作区为准完全同步到 projects 镜像，返回合并 note_table 的 midi_files。

    未绑定时直接返回 prev_midi_files（不同步）。
    """
    ws = get_workspace_dir(project_id)
    if not ws:
        return prev_midi_files

    ws_dir = Path(ws).expanduser().resolve()
    mirror = midi_dir(project_id).resolve()
    mirror.mkdir(parents=True, exist_ok=True)

    ws_files = {f["name"]: f for f in scan_midi_files(ws_dir)}
    mirror_files = {f["name"]: f for f in scan_midi_files(mirror)}

    # 工作区有、projects 无 -> 复制；都有但内容不同 -> 工作区覆盖
    for rel, wf in ws_files.items():
        src = Path(wf["path"])
        mf = mirror_files.get(rel)
        if mf is None:
            dst = mirror / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, dst)
        else:
            mp = Path(mf["path"])
            try:
                same = (
                    src.stat().st_size == mp.stat().st_size
                    and int(src.stat().st_mtime) == int(mp.stat().st_mtime)
                )
            except OSError:
                same = False
            if not same:
                shutil.copy2(src, mp)

    # projects 有、工作区无 -> 删除镜像文件及空父目录
    for rel, mf in mirror_files.items():
        if rel not in ws_files:
            mp = Path(mf["path"])
            try:
                mp.unlink()
            except OSError:
                pass
            parent = mp.parent
            while parent != mirror and parent.is_dir():
                try:
                    parent.rmdir()
                    parent = parent.parent
                except OSError:
                    break

    return _merge_note_table(scan_midi_files(ws_dir), prev_midi_files)


def _normalize_copied_midi_paths(pdir: Path, project_id: str) -> None:
    """把 history.json 的 midi_files.path 标准化为本项目 midi 目录路径。

    用于 copy_project：源项目可能绑定了工作区，复制后 path 仍指向源路径，
    这里统一改写为新项目 projects/<id>/midi/<name>。
    """
    history_file = pdir / "history.json"
    if not history_file.exists():
        return
    try:
        data = json.loads(history_file.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        return
    base = midi_dir(project_id).resolve()
    changed = False
    for mf in data.get("midi_files", []):
        name = mf.get("name", "")
        if name:
            new_path = str((base / name).resolve())
            if mf.get("path") != new_path:
                mf["path"] = new_path
                changed = True
    if not changed:
        return
    tmp = history_file.with_suffix(".tmp")
    try:
        tmp.write_text(
            json.dumps(data, ensure_ascii=False, indent=2), encoding="utf-8",
        )
        tmp.replace(history_file)
    except OSError:
        logger.exception("标准化复制项目路径失败: %s", pdir)
        if tmp.exists():
            tmp.unlink()


def _rewrite_history_paths(project_dir: Path, old_project_dir: Path) -> None:
    """重写 history.json 中所有 midi_files[].path，将旧项目路径替换为新路径。

    同时处理绝对路径和相对路径两种形式。
    """
    history_file = project_dir / "history.json"
    if not history_file.exists():
        return

    try:
        history_data = json.loads(history_file.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError):
        logger.exception("读取项目历史文件失败: %s", history_file)
        return

    old_id = old_project_dir.name
    new_id = project_dir.name
    old_abs = str(old_project_dir)
    new_abs = str(project_dir)

    for mf in history_data.get("midi_files", []):
        path = mf.get("path", "")
        if not path:
            continue

        # 1. 替换绝对路径
        rewritten = path.replace(old_abs, new_abs)

        # 2. 替换相对路径中的项目 ID（作为目录组件）
        if rewritten == path:
            # 将路径按分隔符拆分，替换匹配的项目 ID 组件
            parts = Path(path).parts
            new_parts = [new_id if p == old_id else p for p in parts]
            if tuple(new_parts) != parts:
                rewritten = str(Path(*new_parts))

        mf["path"] = rewritten

    tmp = history_file.with_suffix(".tmp")
    try:
        tmp.write_text(
            json.dumps(history_data, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        tmp.replace(history_file)
    except OSError:
        logger.exception("重写项目历史路径失败: %s", project_dir)
        if tmp.exists():
            tmp.unlink()


# ==================== 项目 CRUD ====================

def create_project(name: str) -> dict:
    """创建新项目目录、meta.json、空 history.json，并更新索引。

    返回项目元数据字典（含 'id'）。
    """
    _ensure_dir()
    project_id = uuid.uuid4().hex[:12]
    now = _now_str()

    meta = {
        "id": project_id,
        "name": name,
        "created_at": now,
        "updated_at": now,
    }

    # 创建目录结构
    pdir = project_dir(project_id)
    (pdir / "midi").mkdir(parents=True, exist_ok=True)

    # 写 meta.json
    (pdir / "meta.json").write_text(
        json.dumps(meta, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )

    # 写空 history.json
    (pdir / "history.json").write_text(
        json.dumps({"messages": [], "midi_files": []}, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )

    # 更新索引
    index = _load_index()
    index.append({
        "id": project_id,
        "name": name,
        "created_at": now,
        "updated_at": now,
        "message_count": 0,
        "midi_count": 0,
    })
    _save_index(index)

    logger.info("项目已创建: %s (%s)", name, project_id)
    return meta


def delete_project(project_id: str) -> None:
    """删除项目目录并从索引中移除。先更新索引，避免异常时留下死引用。"""
    _remove_index_entry(project_id)
    pdir = project_dir(project_id)
    if pdir.exists():
        shutil.rmtree(pdir)
    logger.info("项目已删除: %s", project_id)


def rename_project(project_id: str, new_name: str) -> None:
    """重命名项目，更新 meta.json 和索引。"""
    pdir = project_dir(project_id)
    meta_file = pdir / "meta.json"
    if meta_file.exists():
        meta = json.loads(meta_file.read_text(encoding="utf-8"))
        meta["name"] = new_name
        meta_file.write_text(
            json.dumps(meta, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
    _update_index_entry(project_id, name=new_name)
    logger.info("项目已重命名: %s -> %s", project_id, new_name)


def copy_project(source_id: str, new_name: str) -> dict:
    """深拷贝项目（目录、历史、MIDI 文件），使用新 UUID。

    返回新项目的元数据字典。
    """
    src_dir = project_dir(source_id)
    if not src_dir.exists():
        raise FileNotFoundError(f"源项目不存在: {source_id}")

    new_id = uuid.uuid4().hex[:12]
    now = _now_str()
    dst_dir = project_dir(new_id)

    shutil.copytree(src_dir, dst_dir)

    # 重写 history.json 中 midi_files 的路径，指向新项目目录
    _rewrite_history_paths(dst_dir, src_dir)
    # 源项目可能绑定了工作区，标准化 midi_files.path 指向新项目 midi 目录
    _normalize_copied_midi_paths(dst_dir, new_id)

    # 更新新项目的 meta.json
    meta = {
        "id": new_id,
        "name": new_name,
        "created_at": now,
        "updated_at": now,
    }
    (dst_dir / "meta.json").write_text(
        json.dumps(meta, ensure_ascii=False, indent=2),
        encoding="utf-8",
    )

    # 更新索引
    index = _load_index()
    src_entry = next((e for e in index if e["id"] == source_id), {})
    index.append({
        "id": new_id,
        "name": new_name,
        "created_at": now,
        "updated_at": now,
        "message_count": src_entry.get("message_count", 0),
        "midi_count": src_entry.get("midi_count", 0),
    })
    _save_index(index)

    logger.info("项目已复制: %s -> %s (%s)", source_id, new_name, new_id)
    return meta


# ==================== 列表和加载 ====================

def list_projects() -> list[dict]:
    """返回项目列表，按更新时间降序排列。"""
    index = _load_index()
    index.sort(key=lambda x: x.get("updated_at", ""), reverse=True)
    return index


def load_project(project_id: str) -> dict:
    """加载项目的 meta.json，返回元数据字典。"""
    meta_file = project_dir(project_id) / "meta.json"
    if not meta_file.exists():
        return {}
    try:
        return json.loads(meta_file.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        logger.exception("读取项目元数据失败: %s", project_id)
        return {}


# ==================== 历史持久化 ====================

def save_history(project_id: str, messages: list[dict], midi_files: list[dict]) -> None:
    """将聊天历史和 MIDI 文件元数据持久化到 history.json。

    同时更新索引中的消息数、MIDI 文件数和更新时间。
    """
    pdir = project_dir(project_id)
    pdir.mkdir(parents=True, exist_ok=True)

    # 过滤 midi_files 中的 note_table（太大，不存历史，由 MIDI 文件本身保存）
    midi_meta = []
    for f in midi_files:
        midi_meta.append({
            "name": f.get("name", ""),
            "path": f.get("path", ""),
            "size": f.get("size", 0),
            "note_table": f.get("note_table", ""),
        })

    data = {"messages": messages, "midi_files": midi_meta}

    history_file = pdir / "history.json"
    tmp = history_file.with_suffix(".tmp")
    try:
        tmp.write_text(
            json.dumps(data, ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        tmp.replace(history_file)
    except OSError:
        logger.exception("保存聊天历史失败: %s", project_id)
        if tmp.exists():
            tmp.unlink()
        return

    # 更新索引
    now = _now_str()
    _update_index_entry(
        project_id,
        updated_at=now,
        message_count=len(messages),
        midi_count=len(midi_meta),
    )


def load_history(project_id: str) -> tuple[list[dict], list[dict]]:
    """加载项目的聊天历史和 MIDI 文件元数据。

    返回 (messages, midi_files)。文件不存在时返回空列表。
    """
    history_file = project_dir(project_id) / "history.json"
    if not history_file.exists():
        return [], []
    try:
        data = json.loads(history_file.read_text(encoding="utf-8"))
        return data.get("messages", []), data.get("midi_files", [])
    except (OSError, json.JSONDecodeError):
        logger.exception("读取聊天历史失败: %s", project_id)
        return [], []


# ==================== 对话草稿 ====================

def save_draft(project_id: str, text: str) -> None:
    """持久化项目对话输入框草稿到 draft.txt（空文本也允许，用于清除）。"""
    pdir = project_dir(project_id)
    pdir.mkdir(parents=True, exist_ok=True)
    try:
        (pdir / config.DRAFT_FILENAME).write_text(text or "", encoding="utf-8")
    except OSError:
        logger.exception("保存草稿失败: %s", project_id)


def load_draft(project_id: str) -> str:
    """读取项目对话输入框草稿；无草稿或读取失败返回空串。"""
    try:
        return (project_dir(project_id) / config.DRAFT_FILENAME).read_text(encoding="utf-8")
    except OSError:
        return ""


# ==================== 搜索 ====================

def search_projects(query: str) -> tuple[list[list[str]], list[str]]:
    """在所有项目的对话历史中搜索关键词（大小写不敏感）。

    同时也会搜索项目名称。

    返回 (table_rows, project_ids)。
    table_rows 为 [[项目名, 匹配片段], ...]，
    project_ids 为对应行的项目 ID。
    """
    if not query.strip() or not PROJECTS_DIR.exists():
        return [], []

    query_lower = query.lower()
    index = _load_index()
    id_to_name = {p["id"]: p["name"] for p in index}

    results: list[list[str]] = []
    result_ids: list[str] = []
    seen_pids = set()

    for child in PROJECTS_DIR.iterdir():
        if not child.is_dir():
            continue
        history_file = child / "history.json"
        if not history_file.exists():
            continue

        try:
            data = json.loads(history_file.read_text(encoding="utf-8"))
        except (json.JSONDecodeError, OSError):
            continue

        pid = child.name
        pname = id_to_name.get(pid, pid)

        # 项目名称匹配
        if query_lower in pname.lower() and pid not in seen_pids:
            results.append([pname, "（项目名称匹配）"])
            result_ids.append(pid)
            seen_pids.add(pid)
            continue  # 已匹配，不再检查对话内容

        # 对话内容匹配
        for msg in data.get("messages", []):
            content = msg.get("content", "") or ""
            if query_lower in content.lower() and pid not in seen_pids:
                idx = content.lower().index(query_lower)
                start = max(0, idx - 30)
                end = min(len(content), idx + len(query) + 30)
                snippet = (
                    ("..." if start > 0 else "")
                    + content[start:end]
                    + ("..." if end < len(content) else "")
                )
                results.append([pname, snippet])
                result_ids.append(pid)
                seen_pids.add(pid)
                break  # 每个项目只取第一个匹配

    return results, result_ids
