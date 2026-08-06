"""FastAPI 后端服务。

提供设置/模型、MIDI 解析与 AI 任务（SSE 流式）、项目与对话（SSE 流式）、
文件下载与静态前端挂载。所有业务逻辑委托给 config / get / out / ai_api /
chat_service / project_manager。
"""
from __future__ import annotations

import logging
import os
import shutil
import tempfile
import time
from pathlib import Path
from urllib.parse import quote

from fastapi import FastAPI, File, HTTPException, UploadFile
from fastapi.responses import FileResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel

import ai_api
import chat_service
import config
import get
import out
from out import EmptyNoteTableError

logger = logging.getLogger("ai_midi")

app = FastAPI(title="AI_MIDI", docs_url=None, redoc_url=None, openapi_url=None)

# ==================== 常量（功能选择） ====================
FUNC_ADD_CHORD = "配和弦"
FUNC_TRANSLATE = "翻译歌词"
FUNC_MELISMA = "设计转音"
FUNC_OTHER = "其他要求"


def _build_allowed_paths() -> list[Path]:
    """允许下载的根目录列表（输出/中间产物/项目库）。"""
    return [config.OUTPUT_DIR, config.DOING_DIR, config.PROJECTS_DIR]


# ==================== 设置 ====================

class SettingsIn(BaseModel):
    api_key: str = ""
    base_url: str = ""
    api_path: str = ""
    model: str = ""
    max_tokens: int | None = None
    max_completion_tokens: int | None = None
    reasoning_effort: str = ""
    thinking_enabled: bool = True


@app.get("/api/health")
def health() -> dict:
    return {"ok": True}


@app.get("/api/settings")
def get_settings() -> dict:
    settings = config.load_settings()
    return {
        "api_key": settings.get("api_key", ""),
        "base_url": settings.get("base_url", config.BASE_URL),
        "api_path": settings.get("api_path", config.API_PATH),
        "model": settings.get("model", config.MODEL),
        "max_tokens": settings.get("max_tokens"),
        "max_completion_tokens": settings.get("max_completion_tokens"),
        "reasoning_effort": settings.get("reasoning_effort", "max"),
        "thinking_enabled": settings.get("thinking_enabled", True),
    }


@app.put("/api/settings")
def put_settings(body: SettingsIn) -> dict:
    try:
        config.validate_base_url(body.base_url.strip(), body.api_path.strip())
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))

    def _to_int(value) -> int | None:
        if value is None or value == "":
            return None
        try:
            return int(value)
        except (ValueError, TypeError):
            return None

    settings = {
        "api_key": body.api_key.strip(),
        "base_url": body.base_url.strip(),
        "api_path": body.api_path.strip(),
        "model": body.model.strip(),
        "max_tokens": _to_int(body.max_tokens),
        "max_completion_tokens": _to_int(body.max_completion_tokens),
        "reasoning_effort": body.reasoning_effort.strip(),
        "thinking_enabled": bool(body.thinking_enabled),
    }
    config.save_settings(settings)
    return {"ok": True, "message": "✓ 配置已保存"}


@app.get("/api/models")
def fetch_models(api_key: str = "", base_url: str = "", api_path: str = "") -> dict:
    key = api_key.strip() or config.get_api_key()
    url = base_url.strip() or config.BASE_URL
    path = api_path.strip()

    if not key:
        return {"models": [], "message": "⚠ 请先填写并保存 API Key"}

    try:
        full_url = config.validate_base_url(url, path)
    except ValueError:
        return {"models": [], "message": "✗ 配置错误，请检查 Base URL 与 API 路径。"}

    try:
        client = ai_api.get_client(api_key=key, base_url=full_url)
        models = client.models.list()
        ids = sorted([m.id for m in models.data])
        if not ids:
            return {"models": [], "message": "⚠ 未获取到任何模型"}
        return {"models": ids, "message": f"✓ 已获取 {len(ids)} 个模型"}
    except Exception:  # noqa: BLE001
        logger.exception("获取模型列表失败")
        return {"models": [], "message": "✗ 获取模型失败，请检查网络或 API Key。"}


# ==================== MIDI 解析 ====================

@app.post("/api/parse")
async def parse_midi(file: UploadFile = File(...)) -> dict:
    """上传并解析 MIDI 文件，返回 note_table。"""
    suffix = Path(file.filename or "in.mid").suffix.lower()
    if suffix not in (".mid", ".midi"):
        raise HTTPException(status_code=400, detail="仅支持 .mid / .midi 文件")

    os.makedirs(config.INPUT_MIDI.parent, exist_ok=True)
    try:
        with open(config.INPUT_MIDI, "wb") as f:
            shutil.copyfileobj(file.file, f)
    except OSError:
        logger.exception("保存上传 MIDI 失败")
        raise HTTPException(status_code=500, detail="文件保存失败")

    try:
        note_table = get.get_note(str(config.INPUT_MIDI), save_to_file=False)
    except (OSError, ValueError):
        logger.exception("MIDI 解析失败")
        raise HTTPException(status_code=400, detail="解析失败，请检查 MIDI 文件后重试。")

    if not note_table:
        raise HTTPException(status_code=400, detail="未解析出音符，请检查 MIDI 文件是否有效。")

    return {"status": f"✓ 已解析 {len(note_table)} 个音符", "note_count": len(note_table), "note_table": note_table}


# ==================== AI 任务（SSE） ====================

class RunIn(BaseModel):
    func: str = FUNC_ADD_CHORD
    note_table: list[str] = []
    bpm: str = ""
    time_signature: str = ""
    lyrics: str = ""
    original_language: str = ""
    target_language: str = ""
    note_output: bool = False
    requirements: str = ""


def _sse(data: dict) -> str:
    return f"data: {json_dumps(data)}\n\n"


def json_dumps(data: dict) -> str:
    import json

    return json.dumps(data, ensure_ascii=False)


def _validate_bpm(bpm) -> str:
    try:
        bpm = int(bpm)
        return str(max(config.BPM_MIN, min(config.BPM_MAX, bpm)))
    except (ValueError, TypeError):
        return str(config.DEFAULT_BPM)


def _is_valid_time_signature(ts: str) -> bool:
    import re

    return bool(re.match(r"^\d+/\d+$", ts.strip()))


def _validate_time_signature(ts: str) -> str:
    import re

    if re.match(r"^\d+/\d+$", ts.strip()):
        return ts.strip()
    return config.DEFAULT_TIME_SIGNATURE


def _validate_int_param(value, default, min_val, max_val):
    try:
        v = int(value)
        return max(min_val, min(max_val, v))
    except (ValueError, TypeError):
        return default


def run_task_stream(body: RunIn):
    """AI 任务 SSE 流：progress 事件 + 最终 done 事件。"""
    start_time = time.time()

    def _elapsed(msg: str) -> str:
        return f"{msg}（耗时 {time.time() - start_time:.2f} 秒）"

    # 除"其他要求"外,其余功能都需要先解析 MIDI
    if body.func != FUNC_OTHER and not body.note_table:
        yield _sse({"type": "error", "message": _elapsed("⚠ 请先解析 MIDI 文件。")})
        return

    # 其他要求无需解析 MIDI：进度文案与实际步骤对应，避免误导
    if body.func == FUNC_OTHER:
        yield _sse({"type": "progress", "value": 0.2, "desc": "准备请求"})
    else:
        yield _sse({"type": "progress", "value": 0.2, "desc": "解析 MIDI"})

    settings = chat_service._load_settings()
    if not settings["api_key"]:
        yield _sse({"type": "error", "message": _elapsed("⚠ 请先在设置页填写并保存 API Key。")})
        return

    bpm = _validate_bpm(body.bpm.strip() or str(config.DEFAULT_BPM))
    ts_raw = body.time_signature.strip()
    if ts_raw and not _is_valid_time_signature(ts_raw):
        yield _sse({"type": "warn", "message": f"拍号格式无效 '{ts_raw}'，已回退到 {config.DEFAULT_TIME_SIGNATURE}"})
    time_signature = _validate_time_signature(ts_raw or config.DEFAULT_TIME_SIGNATURE)
    note_text = "\n".join(body.note_table)

    # 组装公共 API 参数（模型等设置来自设置页，与对话共用）
    api_kwargs: dict = {}
    api_kwargs["api_key"] = settings["api_key"]
    if settings.get("base_url"):
        api_kwargs["base_url"] = settings["base_url"]
    if settings.get("model"):
        api_kwargs["model"] = settings["model"]
    if settings.get("max_tokens"):
        api_kwargs["max_tokens"] = _validate_int_param(
            settings["max_tokens"], config.DEFAULT_MAX_TOKENS, config.MAX_TOKENS_MIN, config.MAX_TOKENS_MAX
        )
    if settings.get("max_completion_tokens"):
        api_kwargs["max_completion_tokens"] = _validate_int_param(
            settings["max_completion_tokens"], config.DEFAULT_MAX_TOKENS, config.MAX_TOKENS_MIN, config.MAX_TOKENS_MAX
        )
    if settings.get("reasoning_effort"):
        api_kwargs["reasoning_effort"] = settings["reasoning_effort"]
    api_kwargs["thinking_enabled"] = settings.get("thinking_enabled", True)

    yield _sse({"type": "progress", "value": 0.5, "desc": "调用 AI"})

    try:
        if body.func == FUNC_ADD_CHORD:
            result = ai_api.add_chord(note_text, bpm, time_signature, body.requirements, **api_kwargs)
        elif body.func == FUNC_TRANSLATE:
            if not body.original_language.strip() or not body.target_language.strip():
                yield _sse({"type": "error", "message": _elapsed("⚠ 请填写原语言和目标语言。")})
                return
            result = ai_api.translate_lyrics(
                note_text, body.lyrics, bpm, time_signature,
                body.original_language, body.target_language, **api_kwargs
            )
        elif body.func == FUNC_MELISMA:
            result = ai_api.design_melisma(
                note_text, body.lyrics, bpm, time_signature, body.requirements, **api_kwargs
            )
        else:  # FUNC_OTHER
            if not body.requirements.strip():
                yield _sse({"type": "error", "message": _elapsed("⚠ 请填写具体要求。")})
                return
            result = ai_api.other_requirements(
                note_text, body.lyrics, bpm, time_signature,
                body.requirements, body.note_output, **api_kwargs
            )
    except Exception:  # noqa: BLE001
        logger.exception("AI API 调用失败")
        yield _sse({"type": "error", "message": _elapsed("✗ 调用失败，请稍后重试。")})
        return

    if not result:
        yield _sse({"type": "error", "message": _elapsed("✗ AI 未返回内容或调用失败。")})
        return

    yield _sse({"type": "progress", "value": 0.9, "desc": "保存结果"})

    os.makedirs(config.OUTPUT_DIR, exist_ok=True)
    download_path: str | None = None
    status_msg = ""

    try:
        if body.func == FUNC_TRANSLATE:
            save_path = config.OUTPUT_DIR / "translated_lyrics.txt"
            save_path.write_text(result, encoding="utf-8")
            download_path = str(save_path)
            status_msg = "✓ 歌词已保存"
        elif body.func == FUNC_OTHER and not body.note_output:
            save_path = config.OUTPUT_DIR / "other_requirements_result.txt"
            save_path.write_text(result, encoding="utf-8")
            download_path = str(save_path)
            status_msg = "✓ 结果已保存"
        else:
            out.out_note(result, bpm)
            download_path = str(config.OUTPUT_MIDI)
            status_msg = f"✓ MIDI 已生成: {config.OUTPUT_MIDI.name}"
    except EmptyNoteTableError:
        yield _sse({
            "type": "done", "result": result,
            "download_url": None,
            "status": _elapsed("⚠ AI 回复中未解析出有效音符，请检查 AI 输出格式。"),
        })
        return
    except (OSError, ValueError):
        logger.exception("保存结果失败")
        yield _sse({
            "type": "done", "result": result, "download_url": None,
            "status": _elapsed("✓ AI 返回结果，但保存失败，请重试。"),
        })
        return

    yield _sse({
        "type": "done",
        "result": result,
        "download_url": _download_url(download_path) if download_path else None,
        "status": _elapsed(status_msg),
    })


@app.post("/api/run")
def run_task(body: RunIn) -> StreamingResponse:
    return _sse_response(run_task_stream(body))


def _download_url(filepath: str | Path) -> str:
    p = Path(filepath)
    rel = str(p.resolve().relative_to(config.PROJECT_ROOT.resolve()))
    return f"/api/files/download?path={quote(rel)}"


# ==================== 文件下载 ====================

@app.get("/api/files/download")
def download_file(path: str) -> FileResponse:
    allowed_roots = [root.resolve() for root in _build_allowed_paths()]
    target = (config.PROJECT_ROOT / path).resolve()
    if not any(target.is_relative_to(root) for root in allowed_roots):
        raise HTTPException(status_code=403, detail="路径不在允许范围内")
    if not target.is_file():
        raise HTTPException(status_code=404, detail="文件不存在")
    return FileResponse(str(target), filename=target.name)


# ==================== 项目 ====================

class ProjectIn(BaseModel):
    name: str = ""


@app.get("/api/projects")
def list_projects() -> list[dict]:
    return chat_service.refresh_project_list()


@app.post("/api/projects")
def create_project(body: ProjectIn) -> dict:
    return chat_service.create_project(body.name)


@app.put("/api/projects/{project_id}")
def rename_project(project_id: str, body: ProjectIn) -> dict:
    if not body.name.strip():
        raise HTTPException(status_code=400, detail="项目名称不能为空")
    chat_service.rename_project(project_id, body.name)
    return {"ok": True}


@app.delete("/api/projects/{project_id}")
def delete_project(project_id: str) -> dict:
    chat_service.delete_project(project_id)
    return {"ok": True}


@app.post("/api/projects/{project_id}/copy")
def copy_project(project_id: str, body: ProjectIn) -> dict:
    return chat_service.copy_project(project_id, body.name)


@app.get("/api/projects/search")
def search_projects(q: str = "") -> dict:
    rows, ids = project_manager_search(q)
    return {"rows": rows, "ids": ids}


def project_manager_search(q: str):
    import project_manager

    return project_manager.search_projects(q)


@app.get("/api/projects/{project_id}")
def open_project(project_id: str) -> dict:
    return chat_service.open_project(project_id)


class DraftIn(BaseModel):
    text: str = ""


@app.get("/api/projects/{project_id}/draft")
def get_draft(project_id: str) -> dict:
    import project_manager

    return {"text": project_manager.load_draft(project_id)}


@app.post("/api/projects/{project_id}/draft")
def save_draft(project_id: str, body: DraftIn) -> dict:
    import project_manager

    project_manager.save_draft(project_id, body.text)
    return {"ok": True}


@app.post("/api/projects/{project_id}/clear")
def clear_chat(project_id: str) -> dict:
    return chat_service.clear_chat(project_id)


class MessageEditIn(BaseModel):
    index: int = -1


@app.post("/api/projects/{project_id}/messages/edit")
def edit_message(project_id: str, body: MessageEditIn) -> dict:
    """修改模式：截断第 index 条用户消息之后的对话，返回截断列表与原文。"""
    try:
        return chat_service.edit_message(project_id, body.index)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.post("/api/projects/{project_id}/messages/recall")
def recall_edit(project_id: str) -> dict:
    """撤回修改：恢复进入编辑模式前的对话。"""
    return chat_service.recall_edit(project_id)


@app.post("/api/projects/{project_id}/messages/edit-info")
def message_edit_info(project_id: str, body: MessageEditIn) -> dict:
    """查询该条消息是否有可撤回的修改历史（前端据此启用「撤回修改」选项）。"""
    try:
        return chat_service.edit_info(project_id, body.index)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.post("/api/projects/{project_id}/messages/undo-edit")
def undo_edit(project_id: str, body: MessageEditIn) -> dict:
    """撤回修改：把所有内容退回到这条消息发送之前，再进入修改模式。"""
    try:
        return chat_service.undo_edit(project_id, body.index)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


# ==================== 项目文件管理 ====================

@app.post("/api/projects/{project_id}/files")
async def upload_files(project_id: str, files: list[UploadFile] = File(...)) -> dict:
    """上传 MIDI 文件到项目。"""
    temp_paths: list[str] = []
    orig_names: list[str] = []
    try:
        for f in files:
            suffix = Path(f.filename or "in.mid").suffix.lower()
            if suffix not in (".mid", ".midi"):
                continue
            fd, tmp = tempfile.mkstemp(suffix=suffix)
            os.close(fd)
            with open(tmp, "wb") as out_f:
                shutil.copyfileobj(f.file, out_f)
            temp_paths.append(tmp)
            # 保留原始文件名（mkstemp 临时名不能作为最终文件名）
            orig_names.append(Path(f.filename or "upload.mid").name)
    except OSError:
        logger.exception("接收上传文件失败")
        raise HTTPException(status_code=500, detail="文件保存失败")

    session = chat_service._get_session(project_id)
    prev_count = len(session["midi_files"])
    updated, undo_stack, _ = chat_service._on_upload(
        temp_paths,
        session["midi_files"],
        session["undo_stack"],
        project_id,
        session["full_history"],
        names=orig_names,
    )
    session["midi_files"] = updated
    session["undo_stack"] = undo_stack

    for tmp in temp_paths:
        try:
            os.unlink(tmp)
        except OSError:
            pass

    return {
        "files": chat_service._public_files(updated),
        "added": len(updated) - prev_count,
        "dirs": chat_service._dirs_for(project_id),
    }


class FilesIn(BaseModel):
    names: list[str] = []


class FolderIn(BaseModel):
    name: str = ""
    parent: str = ""


@app.post("/api/projects/{project_id}/folders")
def create_folder(project_id: str, body: FolderIn) -> dict:
    """在当前选中层级下新建文件夹（主目录 + 镜像双写）。"""
    try:
        return chat_service._on_create_folder(project_id, body.name, body.parent)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


class FolderRenameIn(BaseModel):
    old: str = ""
    name: str = ""


@app.post("/api/projects/{project_id}/folders/rename")
def rename_folder(project_id: str, body: FolderRenameIn) -> dict:
    """重命名文件夹（主目录 + 镜像目录级 move，清单前缀同步替换）。"""
    session = chat_service._get_session(project_id)
    updated, undo_stack, error = chat_service._on_rename_folder(
        project_id, body.old, body.name, session["midi_files"],
        session["undo_stack"], session["full_history"],
    )
    if error:
        raise HTTPException(status_code=400, detail=error)
    session["midi_files"] = updated
    session["undo_stack"] = undo_stack
    return {
        "files": chat_service._public_files(updated),
        "dirs": chat_service._dirs_for(project_id),
    }


class FolderDeleteIn(BaseModel):
    folder: str = ""


@app.post("/api/projects/{project_id}/folders/delete")
def delete_folder(project_id: str, body: FolderDeleteIn) -> dict:
    """删除文件夹（显式操作）：内部文件入回收站 + 空目录删除，可撤销。"""
    session = chat_service._get_session(project_id)
    updated, undo_stack, error = chat_service._on_delete_folder(
        project_id, body.folder, session["midi_files"],
        session["undo_stack"], session["full_history"],
    )
    if error:
        raise HTTPException(status_code=400, detail=error)
    session["midi_files"] = updated
    session["undo_stack"] = undo_stack
    return {
        "files": chat_service._public_files(updated),
        "dirs": chat_service._dirs_for(project_id),
    }


class MoveIn(BaseModel):
    moves: list[dict] = []


@app.post("/api/projects/{project_id}/files/move")
def move_files(project_id: str, body: MoveIn) -> dict:
    """移动文件到其他文件夹（target 为空串表示根目录）。"""
    session = chat_service._get_session(project_id)
    updated, undo_stack, failed = chat_service._on_move(
        project_id, body.moves, session["midi_files"],
        session["undo_stack"], session["full_history"],
    )
    session["midi_files"] = updated
    session["undo_stack"] = undo_stack
    return {
        "files": chat_service._public_files(updated),
        "failed": failed,
        "dirs": chat_service._dirs_for(project_id),
    }


@app.delete("/api/projects/{project_id}/files")
def delete_files(project_id: str, body: FilesIn) -> dict:
    session = chat_service._get_session(project_id)
    updated, undo_stack, _, failed = chat_service._on_delete(
        session["midi_files"], body.names, session["undo_stack"], project_id, session["full_history"],
    )
    session["midi_files"] = updated
    session["undo_stack"] = undo_stack
    return {
        "files": chat_service._public_files(updated),
        "failed": failed,
        "dirs": chat_service._dirs_for(project_id),
    }


@app.get("/api/projects/{project_id}/download")
def download_project_files(project_id: str, names: str = "") -> FileResponse:
    """按文件名（逗号分隔）下载项目文件；空则全部；多个时打包 zip。"""
    name_list = [n for n in names.split(",") if n.strip()] if names else []
    target = chat_service._download_files(project_id, name_list)
    if not target:
        raise HTTPException(status_code=404, detail="没有可下载的文件")
    return FileResponse(target, filename=Path(target).name)


@app.post("/api/projects/{project_id}/undo")
def undo_files(project_id: str) -> dict:
    session = chat_service._get_session(project_id)
    restored, undo_stack, _ = chat_service._on_undo(
        session["undo_stack"], session["midi_files"], project_id, session["full_history"],
    )
    session["midi_files"] = restored
    session["undo_stack"] = undo_stack
    return {
        "files": chat_service._public_files(restored),
        "dirs": chat_service._dirs_for(project_id),
    }


# ==================== 工作区绑定 ====================

@app.post("/api/projects/{project_id}/workspace/pick-folder")
def pick_workspace_folder(project_id: str) -> dict:
    path = chat_service._pick_folder_dialog()
    return {"path": path}


class WorkspaceIn(BaseModel):
    path: str = ""


@app.post("/api/projects/{project_id}/workspace/bind")
def bind_workspace(project_id: str, body: WorkspaceIn) -> dict:
    if not body.path.strip():
        raise HTTPException(status_code=400, detail="请输入工作区目录路径")
    try:
        return chat_service.bind_workspace(project_id, body.path.strip())
    except Exception as exc:  # noqa: BLE001
        logger.exception("绑定工作区失败")
        raise HTTPException(status_code=400, detail=f"绑定失败: {exc}")


@app.post("/api/projects/{project_id}/workspace/unbind")
def unbind_workspace(project_id: str) -> dict:
    return chat_service.unbind_workspace(project_id)


@app.post("/api/projects/{project_id}/workspace/refresh")
def refresh_workspace(project_id: str) -> dict:
    return chat_service.refresh_workspace(project_id)


@app.post("/api/projects/{project_id}/workspace/open")
def open_workspace(project_id: str) -> dict:
    """在系统文件管理器中打开工作区（未绑定则打开默认项目 midi 目录）。"""
    try:
        return chat_service.open_workspace(project_id)
    except OSError as exc:
        logger.exception("打开工作区失败")
        raise HTTPException(status_code=400, detail=f"打开工作区失败: {exc}")


# ==================== 对话（SSE） ====================

class ChatIn(BaseModel):
    project_id: str
    message: str = ""
    edit: bool = False


@app.post("/api/chat")
def chat(body: ChatIn) -> StreamingResponse:
    return _sse_response(chat_service.chat_stream(body.project_id, body.message, edit=body.edit))


def _sse_response(generator) -> StreamingResponse:
    return StreamingResponse(
        generator,
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
        },
    )


# ==================== 静态前端（最后挂载） ====================

class NoCacheStaticFiles(StaticFiles):
    """静态资源不缓存：pywebview 窗口无手动刷新入口，WebView2 会按
    Cache-Control 缓存资源导致改动不生效——加 no-cache 强制每次
    页面加载都重新校验（ETag/Last-Modified 未变时仍走 304，开销极小）"""
    def file_response(self, *args, **kwargs):
        resp = super().file_response(*args, **kwargs)
        resp.headers["Cache-Control"] = "no-cache"
        return resp


if config.WEB_DIR.is_dir():
    app.mount("/", NoCacheStaticFiles(directory=str(config.WEB_DIR), html=True), name="web")
