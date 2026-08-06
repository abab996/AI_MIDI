from __future__ import annotations

import copy
import io
import time
from unittest.mock import patch

import chat_service


class _FakeMCPProc:
    """最小可用的 MCP 进程桩：stdout 立即 EOF（用于 reader 线程测试）。"""

    def __init__(self):
        self.stdout = io.StringIO("")

    def poll(self):
        return None


def _reset_reader_state():
    chat_service._mcp_reader_state["proc"] = None
    chat_service._mcp_reader_state["thread"] = None
    chat_service._mcp_reader_state["queue"] = None


def test_ensure_mcp_reader_starts_without_name_error():
    """回归：_ensure_mcp_reader 启动 reader 线程不得因缺少 queue import 崩溃。"""
    proc = _FakeMCPProc()
    try:
        assert chat_service._ensure_mcp_reader(proc) is True
        # 同一进程再次调用应复用现有线程
        assert chat_service._ensure_mcp_reader(proc) is True
    finally:
        _reset_reader_state()


def test_mcp_recv_returns_none_on_eof():
    """stdout EOF（进程已关闭/崩溃）时 _mcp_recv 返回 None 而非抛异常。"""
    proc = _FakeMCPProc()
    try:
        assert chat_service._mcp_recv(proc, timeout=2) is None
    finally:
        _reset_reader_state()


def test_duplicate_project_names_keep_distinct_id_values():
    projects = [
        {"id": "project-a", "name": "同名项目", "message_count": 0},
        {"id": "project-b", "name": "同名项目", "message_count": 0},
    ]
    with patch.object(chat_service.project_manager, "list_projects", return_value=projects):
        choices, ids = chat_service._refresh_project_list()

    assert choices == [("同名项目", "project-a"), ("同名项目", "project-b")]
    assert ids == ["project-a", "project-b"]
    assert chat_service._resolve_project_id("project-b", ids) == "project-b"


def test_rebuild_display_preserves_reasoning_process():
    history = [
        {"role": "user", "content": "配个和弦"},
        {
            "role": "assistant",
            "content": "好的，以下是和弦：",
            "reasoning_content": "先分析调性……",
        },
        {
            "role": "assistant",
            "content": "工具调用前的说明",
            "reasoning_content": "需要读取知识库……",
            "tool_calls": [
                {
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "read_library_file", "arguments": "{}"},
                }
            ],
        },
        {"role": "tool", "tool_call_id": "call-1", "content": "知识库内容"},
    ]

    display = chat_service._rebuild_display_from_history(history)

    assert "<details>" in display[1]["content"]
    assert "<summary>思考过程</summary>" in display[1]["content"]
    assert "先分析调性……" in display[1]["content"]
    assert "好的，以下是和弦：" in display[1]["content"]
    # 工具调用轮的推理过程同样保留
    assert "需要读取知识库……" in display[2]["content"]
    # 无推理过程的普通消息原样保留
    assert "<details>" not in display[0]["content"]


def test_duplicate_file_labels_are_selected_by_path():
    files = [
        {"name": "same.mid", "path": "A/same.mid", "size": 10},
        {"name": "same.mid", "path": "B/same.mid", "size": 10},
    ]

    assert chat_service._selected_files(files, ["B/same.mid"]) == [files[1]]


def test_delete_persists_file_state_and_creates_undo_snapshot():
    files = [
        {"name": "a.mid", "path": "A/a.mid", "size": 10},
        {"name": "b.mid", "path": "B/b.mid", "size": 20},
    ]
    history = [{"role": "user", "content": "hello"}]

    with patch.object(chat_service, "_persist_project_state") as persist, \
         patch.object(chat_service, "_clear_trash"), \
         patch.object(chat_service, "_move_to_trash", side_effect=lambda pid, f: f.get("name")):
        updated, undo_stack, _, failed = chat_service._on_delete(
            files, ["B/b.mid"], [], "project-id", history,
        )

    assert updated == [files[0]]
    # 新格式撤销条目：清单快照 + 回收站相对路径
    assert undo_stack[0]["files"] == files
    assert undo_stack[0]["trash"] == ["b.mid"]
    assert failed == []
    persist.assert_called_once_with("project-id", history, updated)


def test_delete_moves_file_to_trash_and_undo_restores(tmp_path, monkeypatch):
    """删除把磁盘文件移入回收站；撤销移回磁盘（绑定/未绑定通用）。"""
    import project_manager

    pid = "pid-trash-1"
    base = tmp_path / "base"
    sub = base / "sub"
    sub.mkdir(parents=True)
    f = sub / "a.mid"
    f.write_bytes(b"data")
    files = [{"name": "sub/a.mid", "path": str(f), "size": 4}]
    history = []

    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "project_dir", lambda pid_: tmp_path)

    with patch.object(chat_service, "_persist_project_state"):
        updated, undo_stack, _, failed = chat_service._on_delete(
            files, ["sub/a.mid"], [], pid, history,
        )
    assert updated == []
    assert not f.exists()
    assert (tmp_path / ".trash" / "sub" / "a.mid").is_file()
    assert failed == []
    # 文件夹与文件完全独立：删除文件后源空文件夹保留
    assert sub.is_dir()

    with patch.object(chat_service, "_persist_project_state"):
        restored, _, _ = chat_service._on_undo(undo_stack, updated, pid, history)
    assert restored == files
    assert f.is_file()
    assert not (tmp_path / ".trash").exists()


def test_delete_partial_failure_keeps_file_in_list():
    """磁盘删除失败（如被占用）的文件保留在清单并计入 failed。"""
    files = [
        {"name": "a.mid", "path": "A/a.mid", "size": 10},
        {"name": "b.mid", "path": "B/b.mid", "size": 20},
    ]
    with patch.object(chat_service, "_clear_trash"), \
         patch.object(chat_service, "_persist_project_state"), \
         patch.object(chat_service, "_move_to_trash",
                      side_effect=lambda pid, f: f.get("name") if f["name"] == "a.mid" else None):
        updated, undo_stack, _, failed = chat_service._on_delete(
            files, ["A/a.mid", "B/b.mid"], [], "pid-x", [],
        )
    assert [f["name"] for f in updated] == ["b.mid"]
    assert failed == ["b.mid"]
    assert undo_stack[0]["trash"] == ["a.mid"]
    assert undo_stack[0]["files"] == files


def test_delete_all_failed_no_change():
    """全部删除失败：清单不变、不产生撤销条目、不持久化。"""
    files = [{"name": "a.mid", "path": "A/a.mid", "size": 10}]
    with patch.object(chat_service, "_clear_trash"), \
         patch.object(chat_service, "_persist_project_state") as persist, \
         patch.object(chat_service, "_move_to_trash", return_value=None):
        updated, undo_stack, _, failed = chat_service._on_delete(
            files, ["a.mid"], [], "pid-x", [],
        )
    assert updated == files
    assert undo_stack == []
    assert failed == ["a.mid"]
    persist.assert_not_called()


def test_undo_legacy_plain_list_entry():
    """旧格式撤销条目（纯清单快照）仍可恢复，不触碰回收站。"""
    expected = [{"name": "a.mid", "path": "A/a.mid", "size": 10}]
    # 用深拷贝构造栈：若未来 _on_undo 改为原地修改内层列表，
    # expected 与被测返回值是同一对象会"自证其真"，掩盖回归
    legacy_stack = [copy.deepcopy(expected)]
    with patch.object(chat_service, "_persist_project_state") as persist:
        restored, stack, _ = chat_service._on_undo(legacy_stack, [], "pid-x", [])
    assert restored == expected
    assert restored is not expected
    assert stack == []
    persist.assert_called_once_with("pid-x", [], restored)


def test_clear_chat_keeps_midi_metadata():
    files = [{"name": "song.mid", "path": "song.mid", "size": 10}]
    with patch.object(chat_service.project_manager, "save_history") as save:
        assert chat_service._on_clear_chat("project-id", files) == ([], [])
    save.assert_called_once_with("project-id", [], files)


def test_created_midi_path_rejects_output_escape(tmp_path):
    with patch.object(chat_service, "_current_project_id", None), patch.object(
        chat_service.config, "OUTPUT_DIR", tmp_path,
    ):
        try:
            chat_service._resolve_created_midi_path("../escape.mid")
        except ValueError:
            pass
        else:
            raise AssertionError("path traversal should be rejected")


# ==================== 消息编辑（修改 / 撤回） ====================

def _seed_session(project_id, display, full_history, midi_files=None):
    """向内存会话表注入指定状态（隔离项目文件，不触盘）。"""
    chat_service._sessions[project_id] = {
        "full_history": full_history,
        "midi_files": midi_files or [],
        "undo_stack": [],
        "chat_display": display,
        "pending_edit": None,
        "edit_history": [],
    }


def test_edit_message_truncates_display_and_history():
    pid = "pid-edit-1"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
    ]
    full_history = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A", "reasoning_content": "思考A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B", "reasoning_content": "思考B"},
        {"role": "tool", "tool_call_id": "c1", "content": "工具结果"},
    ]
    try:
        _seed_session(pid, display, full_history)
        result = chat_service.edit_message(pid, 2)  # 编辑「问题B」

        assert result["text"] == "问题B"
        # 显示列表截断到被编辑消息（含）
        assert result["messages"] == display[:3]
        # 内存历史截断到第 3 条 user 消息（含），其后全部移除
        assert chat_service._get_session(pid)["full_history"] == full_history[:3]
        # 快照保存进入编辑前的完整状态，供撤回恢复
        pending = chat_service._get_session(pid)["pending_edit"]
        assert pending["index"] == 2
        assert pending["text"] == "问题B"
        assert pending["full_history"] == full_history
        assert pending["chat_display"] == display
    finally:
        chat_service._sessions.pop(pid, None)


def test_edit_message_rejects_non_user_or_bad_index():
    pid = "pid-edit-2"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
    ]
    try:
        _seed_session(pid, display, list(display))
        for bad in (-1, 5, 1):  # 越界 / 指向 AI 消息
            try:
                chat_service.edit_message(pid, bad)
            except ValueError:
                pass
            else:
                raise AssertionError(f"index {bad} 应被拒绝")
        # 拒绝时不得产生快照或改动
        session = chat_service._get_session(pid)
        assert session["pending_edit"] is None
        assert session["chat_display"] == display
    finally:
        chat_service._sessions.pop(pid, None)


def test_recall_edit_restores_truncated_conversation():
    pid = "pid-edit-3"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
    ]
    full_history = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
    ]
    try:
        _seed_session(pid, display, full_history)
        chat_service.edit_message(pid, 2)  # 编辑「问题B」，截断其后
        assert len(chat_service._get_session(pid)["chat_display"]) == 3

        with patch.object(chat_service, "_persist_project_state") as persist:
            result = chat_service.recall_edit(pid)

        assert result["messages"] == display
        session = chat_service._get_session(pid)
        assert session["chat_display"] == display
        assert session["full_history"] == full_history
        assert session.get("pending_edit") is None
        persist.assert_called_once_with(pid, full_history, [])
    finally:
        chat_service._sessions.pop(pid, None)


def test_recall_edit_without_pending_is_idempotent():
    pid = "pid-edit-4"
    display = [{"role": "user", "content": "问题A"}]
    try:
        _seed_session(pid, display, list(display))
        result = chat_service.recall_edit(pid)
        assert result["messages"] == display
    finally:
        chat_service._sessions.pop(pid, None)


@patch.object(chat_service.project_manager, "save_edit_history")
def test_edit_history_dedup_and_limit(_save):
    """修改历史栈：同一条消息只保留最近快照，且限长防膨胀。"""
    pid = "pid-history-1"
    try:
        _seed_session(pid, [], [])
        session = chat_service._get_session(pid)
        # 同 index 循环压栈：去重后各 index 仅剩最近一条
        for i in range(15):
            chat_service._push_edit_history(pid, session, {
                "index": i % 3, "text": str(i),
                "full_history": [], "chat_display": [],
            })
        history = session["edit_history"]
        assert sorted(h["index"] for h in history) == [0, 1, 2]
        assert history[-1]["text"] == "14"  # index 2 的最近记录
        # 不同 index 压栈超过限长：最旧的被挤出
        session["edit_history"] = []
        for i in range(15):
            chat_service._push_edit_history(pid, session, {
                "index": 100 + i, "text": str(i),
                "full_history": [], "chat_display": [],
            })
        history = session["edit_history"]
        assert len(history) == chat_service._EDIT_HISTORY_LIMIT
        assert history[0]["text"] == "5"
        assert history[-1]["text"] == "14"
    finally:
        chat_service._sessions.pop(pid, None)


@patch.object(chat_service.project_manager, "save_edit_history")
def test_undo_edit_restores_state_before_send(_save):
    """撤回修改：把所有内容退回到这条消息发送之前，再进入修改模式。"""
    pid = "pid-undo-1"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
        {"role": "user", "content": "问题C"},
        {"role": "assistant", "content": "回答C"},
    ]
    full_history = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
        {"role": "user", "content": "问题C"},
    ]
    try:
        _seed_session(pid, display, full_history)
        # 用户修改「问题B」并发送：进入编辑前的完整状态移入修改历史
        chat_service.edit_message(pid, 2)
        session = chat_service._get_session(pid)
        chat_service._push_edit_history(pid, session, session.pop("pending_edit"))
        assert len(session["chat_display"]) == 3  # 发送后对话截断在「问题B」

        # 撤回修改：恢复到「问题B」发送前的完整对话，并进入修改模式
        result = chat_service.undo_edit(pid, 2)
        assert result["text"] == "问题B"
        assert result["messages"] == display[:3]
        session = chat_service._get_session(pid)
        assert session["full_history"] == full_history[:3]

        # 编辑条「↩ 撤回」可撤回这次撤回：恢复执行「撤回修改」前的状态
        pending = session["pending_edit"]
        assert pending["index"] == 2
        assert pending["chat_display"] == display[:3]
        with patch.object(chat_service, "_persist_project_state") as persist:
            recalled = chat_service.recall_edit(pid)
        assert recalled["messages"] == display[:3]
        assert chat_service._get_session(pid).get("pending_edit") is None
        persist.assert_called_once()
    finally:
        chat_service._sessions.pop(pid, None)


@patch.object(chat_service.project_manager, "save_edit_history")
def test_undo_edit_restores_later_user_edits_too(_save):
    """修改 B 发送后再修改 C 发送——撤回 B 的修改时，C 的修改一并还原。"""
    pid = "pid-undo-2"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
        {"role": "user", "content": "问题C"},
        {"role": "assistant", "content": "回答C"},
    ]
    full_history = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
        {"role": "user", "content": "问题C"},
    ]
    try:
        _seed_session(pid, display, full_history)
        session = chat_service._get_session(pid)
        # 修改「问题B」并发送：历史1 = 初始完整状态
        chat_service.edit_message(pid, 2)
        chat_service._push_edit_history(pid, session, session.pop("pending_edit"))
        # 继续修改「问题C」并发送：历史2 = 「问题B」修改后的完整状态
        display2 = [
            {"role": "user", "content": "问题A"},
            {"role": "assistant", "content": "回答A"},
            {"role": "user", "content": "问题B改"},
            {"role": "assistant", "content": "回答B改"},
            {"role": "user", "content": "问题C"},
            {"role": "assistant", "content": "回答C"},
        ]
        full2 = [
            {"role": "user", "content": "问题A"},
            {"role": "assistant", "content": "回答A"},
            {"role": "user", "content": "问题B改"},
            {"role": "assistant", "content": "回答B改"},
            {"role": "user", "content": "问题C"},
        ]
        session["chat_display"] = display2
        session["full_history"] = full2
        chat_service.edit_message(pid, 4)
        chat_service._push_edit_history(pid, session, session.pop("pending_edit"))

        # 撤回「问题B」的修改：恢复到 B 发送前的完整对话，C 的修改一并还原
        result = chat_service.undo_edit(pid, 2)
        assert result["text"] == "问题B"
        assert result["messages"] == display[:3]
        assert chat_service._get_session(pid)["full_history"] == full_history[:3]
    finally:
        chat_service._sessions.pop(pid, None)


@patch.object(chat_service.project_manager, "save_edit_history")
def test_edit_info_flags_history(_save):
    """edit_info：无历史返回 False，编辑发送后该消息返回 True。"""
    pid = "pid-info-1"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
    ]
    try:
        _seed_session(pid, display, list(display))
        assert chat_service.edit_info(pid, 0)["has_edit_history"] is False
        assert chat_service.edit_info(pid, 2)["has_edit_history"] is False
        # 编辑发送后，该消息有可撤回的修改
        session = chat_service._get_session(pid)
        chat_service.edit_message(pid, 2)
        chat_service._push_edit_history(pid, session, session.pop("pending_edit"))
        assert chat_service.edit_info(pid, 2)["has_edit_history"] is True
        assert chat_service.edit_info(pid, 0)["has_edit_history"] is False
    finally:
        chat_service._sessions.pop(pid, None)


def test_undo_edit_without_history_rejects():
    """从未修改过的消息：撤回修改被拒绝，且不得改动任何状态。"""
    pid = "pid-undo-3"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
    ]
    try:
        _seed_session(pid, display, list(display))
        try:
            chat_service.undo_edit(pid, 2)
        except ValueError:
            pass
        else:
            raise AssertionError("无历史时撤回修改应被拒绝")
        session = chat_service._get_session(pid)
        assert session["pending_edit"] is None
        assert session["chat_display"] == display
    finally:
        chat_service._sessions.pop(pid, None)


def test_edit_history_persists_across_session_reload(tmp_path, monkeypatch):
    """修改历史落盘：模拟服务重启（session 重建水合）后「撤回修改」仍可用。"""
    monkeypatch.setattr(chat_service.project_manager, "PROJECTS_DIR", tmp_path / "projects")
    pid = "pid-persist-1"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
    ]
    try:
        _seed_session(pid, display, list(display))
        # 编辑发送：快照入历史并落盘；发送后对话也落盘
        chat_service.edit_message(pid, 2)
        session = chat_service._get_session(pid)
        chat_service._push_edit_history(pid, session, session.pop("pending_edit"))
        sent_display = [
            {"role": "user", "content": "问题A"},
            {"role": "assistant", "content": "回答A"},
            {"role": "user", "content": "问题B改"},
            {"role": "assistant", "content": "回答B改"},
        ]
        chat_service.project_manager.save_history(pid, sent_display, [])

        # 模拟服务重启：丢弃内存会话，从磁盘重新水合
        chat_service._sessions.pop(pid, None)
        reloaded = chat_service._get_session(pid)
        assert reloaded["edit_history"]
        assert reloaded["edit_history"][0]["index"] == 2
        assert chat_service.edit_info(pid, 2)["has_edit_history"] is True

        # 撤回修改仍能退回这条消息发送之前
        result = chat_service.undo_edit(pid, 2)
        assert result["text"] == "问题B"
        assert result["messages"] == display[:3]
    finally:
        chat_service._sessions.pop(pid, None)


def test_chat_stream_edit_replaces_last_user_message():
    """编辑发送：被编辑消息被新消息替换，AI 上下文止于截断点。"""
    pid = "pid-edit-5"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
    ]
    full_history = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
        {"role": "assistant", "content": "回答B"},
    ]
    captured = {}

    def fake_prepare(message, history_for_ai, midi_files, settings, history):
        captured["history_for_ai"] = list(history_for_ai)
        captured["display_for_ai"] = list(history)
        return {
            "kwargs": {}, "client": None, "messages": [], "system_prompt": "",
            "_round_start_idx": 0,
            "chat_display": list(history) + [{"role": "user", "content": message}],
            "updated_files": [], "download_path": None, "tool_log": [],
            "max_tool_rounds": 3, "is_gemini": True,
        }

    def fake_loop(ctx, new_undo_stack, history_for_ai, message, project_id=None):
        final_history = history_for_ai + [
            {"role": "user", "content": message},
            {"role": "assistant", "content": "新回复"},
        ]
        yield (
            ctx["chat_display"] + [{"role": "assistant", "content": "新回复"}],
            "", [], new_undo_stack, final_history, None, None,
        )

    try:
        _seed_session(pid, display, full_history)
        chat_service.edit_message(pid, 2)  # 编辑「问题B」

        with patch.object(chat_service, "_load_settings", return_value={"api_key": "test-key"}), \
             patch.object(chat_service.project_manager, "get_workspace_dir", return_value=None), \
             patch.object(chat_service.project_manager, "save_draft"), \
             patch.object(chat_service.project_manager, "save_edit_history"), \
             patch("chat_pipeline._prepare_context", fake_prepare), \
             patch("chat_pipeline._execute_tool_loop", fake_loop):
            events = list(chat_service.chat_stream(pid, "问题B改", edit=True))

        session = chat_service._get_session(pid)
        # 替换语义：被编辑的「问题B」被新消息替换，AI 回复接在后面
        assert [m["content"] for m in session["chat_display"]] == ["问题A", "回答A", "问题B改", "新回复"]
        assert session["full_history"] == [
            {"role": "user", "content": "问题A"},
            {"role": "assistant", "content": "回答A"},
            {"role": "user", "content": "问题B改"},
            {"role": "assistant", "content": "新回复"},
        ]
        # AI 上下文不含被编辑消息（截断点之后的内容已剔除）
        assert captured["history_for_ai"] == full_history[:2]
        assert captured["display_for_ai"] == display[:2]
        # 发送后修改快照移入修改历史（供「撤回修改」还原），当前编辑态清空
        assert session.get("pending_edit") is None
        assert len(session["edit_history"]) == 1
        assert session["edit_history"][0]["index"] == 2
        assert session["edit_history"][0]["text"] == "问题B"
        assert session["edit_history"][0]["full_history"] == full_history
        # 事件流以 done 收尾
        assert events[-1].startswith('data: {"type": "done"}')
    finally:
        chat_service._sessions.pop(pid, None)


def test_chat_stream_normal_send_abandons_pending_edit():
    """未带 edit 标志的普通发送：修改被放弃，消息在截断点后正常追加。"""
    pid = "pid-edit-6"
    display = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
    ]
    full_history = [
        {"role": "user", "content": "问题A"},
        {"role": "assistant", "content": "回答A"},
        {"role": "user", "content": "问题B"},
    ]

    def fake_prepare(message, history_for_ai, midi_files, settings, history):
        return {
            "kwargs": {}, "client": None, "messages": [], "system_prompt": "",
            "_round_start_idx": 0,
            "chat_display": list(history) + [{"role": "user", "content": message}],
            "updated_files": [], "download_path": None, "tool_log": [],
            "max_tool_rounds": 3, "is_gemini": True,
        }

    def fake_loop(ctx, new_undo_stack, history_for_ai, message, project_id=None):
        yield (
            ctx["chat_display"] + [{"role": "assistant", "content": "新回复"}],
            "", [], new_undo_stack,
            history_for_ai + [{"role": "user", "content": message},
                              {"role": "assistant", "content": "新回复"}],
            None, None,
        )

    try:
        _seed_session(pid, display, full_history)
        chat_service.edit_message(pid, 0)  # 进入修改模式（截断到「问题A」）

        with patch.object(chat_service, "_load_settings", return_value={"api_key": "test-key"}), \
             patch.object(chat_service.project_manager, "get_workspace_dir", return_value=None), \
             patch.object(chat_service.project_manager, "save_draft"), \
             patch("chat_pipeline._prepare_context", fake_prepare), \
             patch("chat_pipeline._execute_tool_loop", fake_loop):
            list(chat_service.chat_stream(pid, "问题C", edit=False))

        session = chat_service._get_session(pid)
        assert [m["content"] for m in session["chat_display"]] == ["问题A", "问题C", "新回复"]
        assert session.get("pending_edit") is None
    finally:
        chat_service._sessions.pop(pid, None)


def test_chat_stream_pending_tool_frame_bypasses_throttle():
    """「执行中」占位帧跳过 25ms 节流：快工具也要把标签帧送达前端。"""
    pid = "pid-throttle-1"
    pending_msg = {
        "role": "assistant",
        "content": '<details><summary>🔧 调用 `x()` ⏳</summary>\n'
                   '<div class="tool-pending">⏳ 正在执行…</div></details>',
    }
    entry_msg = {
        "role": "assistant",
        "content": '<details><summary>🔧 调用 `x()`</summary>```\nok\n```</details>',
    }

    def fake_prepare(message, history_for_ai, midi_files, settings, history):
        return {
            "kwargs": {}, "client": None, "messages": [], "system_prompt": "",
            "_round_start_idx": 0,
            "chat_display": list(history) + [{"role": "user", "content": message}],
            "updated_files": [], "download_path": None, "tool_log": [],
            "max_tool_rounds": 3, "is_gemini": True,
        }

    def fake_loop(ctx, new_undo_stack, history_for_ai, message, project_id=None):
        # 两帧间隔 <25ms：占位帧携带 tool-pending 标记必须绕过节流送达
        base = [{"role": "user", "content": message}]
        yield (base + [pending_msg], "", [], new_undo_stack, [], None, None)
        yield (base + [entry_msg], "", [], new_undo_stack, [], None, None)

    try:
        _seed_session(pid, [], [])
        with patch.object(chat_service, "_load_settings", return_value={"api_key": "test-key"}), \
             patch.object(chat_service.project_manager, "get_workspace_dir", return_value=None), \
             patch.object(chat_service.project_manager, "save_draft"), \
             patch("chat_pipeline._prepare_context", fake_prepare), \
             patch("chat_pipeline._execute_tool_loop", fake_loop):
            events = list(chat_service.chat_stream(pid, "问题", edit=False))

        chat_frames = [e for e in events if '"type": "chat"' in e]
        # 至少两帧：占位帧（tool-pending）+ 完成帧
        assert len(chat_frames) >= 2, chat_frames
        first = chat_frames[0]
        assert "tool-pending" in first
        assert "🔧 调用" in first
        # 完成帧无占位标记
        assert "tool-pending" not in chat_frames[1]
    finally:
        chat_service._sessions.pop(pid, None)


# ==================== 文件夹与移动 ====================

def test_create_folder_validates_and_mirrors(tmp_path, monkeypatch):
    """新建文件夹：校验 + 主目录/镜像双写 + 返回目录列表。"""
    import project_manager

    pid = "pid-folder-1"
    base = tmp_path / "base"
    mirror = tmp_path / "mirror"
    base.mkdir()
    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: mirror)

    # 非法名称
    for bad in ("", "a/b", "..", "a\\b"):
        try:
            chat_service._on_create_folder(pid, bad)
        except ValueError:
            pass
        else:
            raise AssertionError(f"名称 {bad!r} 应被拒绝")

    result = chat_service._on_create_folder(pid, "drums")
    assert (base / "drums").is_dir()
    assert (mirror / "drums").is_dir()
    assert "drums" in result["dirs"]

    # 嵌套创建（parent 参数）
    chat_service._on_create_folder(pid, "solo", parent="drums")
    assert (base / "drums" / "solo").is_dir()
    assert (mirror / "drums" / "solo").is_dir()

    # 已存在 → 报错
    try:
        chat_service._on_create_folder(pid, "drums")
    except ValueError as e:
        assert "已存在" in str(e)
    else:
        raise AssertionError("已存在文件夹应报错")


def test_move_file_updates_disk_listing_and_undo(tmp_path, monkeypatch):
    """移动文件：磁盘（主目录+镜像）移动、清单更新、撤销移回。"""
    import project_manager

    pid = "pid-move-1"
    base = tmp_path / "base"
    mirror = tmp_path / "mirror"
    (base / "drums").mkdir(parents=True)
    (mirror / "drums").mkdir(parents=True)
    f = base / "drums" / "a.mid"
    f.write_bytes(b"data")
    mf = mirror / "drums" / "a.mid"
    mf.write_bytes(b"data")
    files = [{"name": "drums/a.mid", "path": str(f), "size": 4, "note_table": "NT"}]
    history = []

    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: mirror)

    with patch.object(chat_service, "_persist_project_state"):
        updated, undo_stack, failed = chat_service._on_move(
            pid, [{"name": "drums/a.mid", "target": ""}], files, [], history,
        )
    assert failed == []
    assert updated[0]["name"] == "a.mid"
    assert updated[0]["path"] == str(base / "a.mid")
    assert updated[0]["note_table"] == "NT"
    # 磁盘：主目录与镜像都已移动；文件夹与文件独立，源空文件夹保留
    assert (base / "a.mid").is_file()
    assert (mirror / "a.mid").is_file()
    assert (base / "drums").is_dir()
    assert (mirror / "drums").is_dir()
    # undo 条目含 moves
    assert undo_stack[0]["moves"] == [{"from": "drums/a.mid", "to": "a.mid"}]

    # 撤销：移回原位置
    with patch.object(chat_service, "_persist_project_state"):
        restored, _, _ = chat_service._on_undo(undo_stack, updated, pid, history)
    assert restored == files
    assert (base / "drums" / "a.mid").is_file()
    assert (mirror / "drums" / "a.mid").is_file()


def test_move_conflict_and_missing_target_fail():
    """移动目标同名冲突/源文件缺失 → 计入 failed 且不改动清单。"""
    files = [
        {"name": "a.mid", "path": "A/a.mid", "size": 10},
        {"name": "drums/b.mid", "path": "D/b.mid", "size": 20},
        {"name": "drums/a.mid", "path": "D2/a.mid", "size": 30},
    ]
    with patch.object(chat_service, "_persist_project_state") as persist, \
         patch.object(chat_service.project_manager, "get_midi_base_dir", return_value=None), \
         patch.object(chat_service.project_manager, "get_midi_mirror_dir", return_value=None), \
         patch.object(chat_service, "_move_to_trash"):
        # a.mid -> drums/ 与已有 drums/a.mid 冲突；缺 name 的移动直接失败
        updated, undo_stack, failed = chat_service._on_move(
            "pid-x", [{"name": "a.mid", "target": "drums"}, {"name": "ghost.mid", "target": ""}],
            files, [], [],
        )
    assert failed == ["a.mid", "ghost.mid"]
    assert updated == files
    assert undo_stack == []
    persist.assert_not_called()


def test_move_rename_field_renames_and_undo(tmp_path, monkeypatch):
    """_on_move 的 rename 字段：同目录改名/移动并改名/非法名失败/冲突失败。"""
    import project_manager

    pid = "pid-rename-1"
    base = tmp_path / "base"
    mirror = tmp_path / "mirror"
    base.mkdir()
    mirror.mkdir()
    f = base / "a.mid"
    f.write_bytes(b"data")
    mf = mirror / "a.mid"
    mf.write_bytes(b"data")
    files = [{"name": "a.mid", "path": str(f), "size": 4, "note_table": "NT"}]
    history = []

    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: mirror)

    # 同目录改名
    with patch.object(chat_service, "_persist_project_state"):
        updated, undo_stack, failed = chat_service._on_move(
            pid, [{"name": "a.mid", "target": "", "rename": "新名字.mid"}],
            files, [], history,
        )
    assert failed == []
    assert updated[0]["name"] == "新名字.mid"
    assert updated[0]["path"] == str(base / "新名字.mid")
    assert updated[0]["note_table"] == "NT"
    assert (base / "新名字.mid").is_file()
    assert (mirror / "新名字.mid").is_file()
    assert not (base / "a.mid").exists()
    assert undo_stack[0]["moves"] == [{"from": "a.mid", "to": "新名字.mid"}]

    # 撤销恢复原名
    with patch.object(chat_service, "_persist_project_state"):
        restored, _, _ = chat_service._on_undo(undo_stack, updated, pid, history)
    assert restored == files
    assert (base / "a.mid").is_file()
    assert (mirror / "a.mid").is_file()

    # 移动并改名：drums/新名.mid
    with patch.object(chat_service, "_persist_project_state"):
        updated, undo_stack, failed = chat_service._on_move(
            pid, [{"name": "a.mid", "target": "drums", "rename": "b.mid"}],
            restored, undo_stack, history,
        )
    assert failed == []
    assert updated[0]["name"] == "drums/b.mid"
    assert (base / "drums" / "b.mid").is_file()
    # 源空文件夹保留（独立性）
    assert (base / "drums").is_dir()

    # 非法新名
    for bad in ("", "x/y.mid", "..", "a\\b.mid"):
        with patch.object(chat_service, "_persist_project_state"):
            _, _, failed = chat_service._on_move(
                pid, [{"name": "drums/b.mid", "target": "", "rename": bad}],
                updated, undo_stack, history,
            )
        assert failed == ["drums/b.mid"], bad
    # 冲突：重命名为清单中已有文件同名
    c = base / "c.mid"
    c.write_bytes(b"data")
    (mirror / "c.mid").write_bytes(b"data")
    files2 = updated + [{"name": "c.mid", "path": str(c), "size": 4}]
    with patch.object(chat_service, "_persist_project_state"):
        _, _, failed = chat_service._on_move(
            pid, [{"name": "drums/b.mid", "target": "", "rename": "c.mid"}],
            files2, undo_stack, history,
        )
    assert failed == ["drums/b.mid"]


def test_move_target_traversal_rejected(tmp_path, monkeypatch):
    """_on_move 的 target 防穿越：.. 路径段/绝对路径一律拒绝，文件原地不动。

    回归：修复前 target 只做 strip 不做 ".." 校验（rename 有校验唯独
    target 漏了），构造 target="../../.." 可把项目文件真实移到目录外。
    """
    import project_manager

    pid = "pid-move-trav"
    base = tmp_path / "base"
    base.mkdir()
    f = base / "a.mid"
    f.write_bytes(b"data")
    files = [{"name": "a.mid", "path": str(f), "size": 4}]
    history = []

    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: None)

    evil_targets = [
        "../..",            # 常规 .. 逃逸
        "..",               # 单段 ..
        "a/../../..",       # 中段 ..
        "..\\..",           # Windows 反斜杠形式
        str(tmp_path / "escape"),  # 绝对路径（pathlib 拼接会整体覆盖 base）
    ]
    for target in evil_targets:
        with patch.object(chat_service, "_persist_project_state") as persist:
            updated, undo_stack, failed = chat_service._on_move(
                pid, [{"name": "a.mid", "target": target}], files, [], history,
            )
        assert failed == ["a.mid"], target
        assert updated == files
        assert undo_stack == []
        persist.assert_not_called()
        # 磁盘文件未被移出项目目录
        assert (base / "a.mid").is_file()
    assert not (tmp_path / "escape").exists()


def test_chat_stream_early_returns_send_done_event():
    """空消息/未配置 API Key 的早期返回也补发 done 事件。

    回归：修复前这两条路径只发 error 直接 return，前端 error 分支不
    收尾、ssePost 正常 resolve 不触发 catch → 界面永久卡死。
    """
    # 空消息（在设置加载之前返回）
    events = list(chat_service.chat_stream("pid-x", "   "))
    assert events[0].startswith('data: {"type": "error"')
    assert events[-1].startswith('data: {"type": "done"}')

    # 未配置 API Key
    with patch.object(chat_service, "_load_settings", return_value={"api_key": ""}):
        events = list(chat_service.chat_stream("pid-x", "你好"))
    assert events[0].startswith('data: {"type": "error"')
    assert events[-1].startswith('data: {"type": "done"}')


def test_push_undo_caps_stack():
    """撤销栈有上限：超限丢弃最旧快照（内存保护，避免长期对话无界增长）。"""
    with patch.object(chat_service, "_clear_trash"):
        stack = []
        for i in range(chat_service._UNDO_STACK_LIMIT + 5):
            stack = chat_service._push_undo(
                "pid-x", stack, [{"name": f"f{i}.mid", "size": 1}],
            )
    assert len(stack) == chat_service._UNDO_STACK_LIMIT
    assert stack[0]["files"][0]["name"] == "f5.mid"      # 最旧 5 个被丢弃
    assert stack[-1]["files"][0]["name"] == f"f{chat_service._UNDO_STACK_LIMIT + 4}.mid"


def test_rename_folder_updates_listing_and_undo(tmp_path, monkeypatch):
    """重命名文件夹：主/镜像目录级 move、清单前缀替换、dir_moves 撤销。"""
    import project_manager

    pid = "pid-rfolder-1"
    base = tmp_path / "base"
    mirror = tmp_path / "mirror"
    (base / "drums").mkdir(parents=True)
    (mirror / "drums").mkdir(parents=True)
    f = base / "drums" / "a.mid"
    f.write_bytes(b"data")
    mf = mirror / "drums" / "a.mid"
    mf.write_bytes(b"data")
    files = [{"name": "drums/a.mid", "path": str(f), "size": 4, "note_table": "NT"}]
    history = []

    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: mirror)

    with patch.object(chat_service, "_persist_project_state"):
        updated, undo_stack, error = chat_service._on_rename_folder(
            pid, "drums", "鼓组", files, [], history,
        )
    assert error == ""
    assert updated[0]["name"] == "鼓组/a.mid"
    assert updated[0]["path"] == str(base / "鼓组" / "a.mid")
    assert updated[0]["note_table"] == "NT"
    assert (base / "鼓组" / "a.mid").is_file()
    assert (mirror / "鼓组" / "a.mid").is_file()
    assert not (base / "drums").exists()
    assert undo_stack[0]["dir_moves"] == [{"from": "drums", "to": "鼓组"}]

    # 撤销：目录整体移回
    with patch.object(chat_service, "_persist_project_state"):
        restored, _, _ = chat_service._on_undo(undo_stack, updated, pid, history)
    assert restored == files
    assert (base / "drums" / "a.mid").is_file()
    assert (mirror / "drums" / "a.mid").is_file()

    # 非法新名 / 目标已存在 / 文件夹不存在 → error 且无变更
    with patch.object(chat_service, "_persist_project_state") as persist:
        updated2, stack2, err = chat_service._on_rename_folder(
            pid, "drums", "a/b", files, [], history,
        )
    assert err
    assert updated2 == files and stack2 == []
    persist.assert_not_called()
    (base / "other").mkdir()
    with patch.object(chat_service, "_persist_project_state"):
        _, _, err = chat_service._on_rename_folder(pid, "drums", "other", files, [], history)
    assert "已存在" in err
    with patch.object(chat_service, "_persist_project_state"):
        _, _, err = chat_service._on_rename_folder(pid, "ghost", "x", files, [], history)
    assert "不存在" in err


def test_delete_folder_moves_files_to_trash_and_undo(tmp_path, monkeypatch):
    """删除文件夹：内部文件入回收站、目录（含空子目录）删除、撤销完整重建。"""
    import project_manager

    pid = "pid-dfolder-1"
    base = tmp_path / "base"
    mirror = tmp_path / "mirror"
    (base / "drums" / "sub").mkdir(parents=True)
    (mirror / "drums" / "sub").mkdir(parents=True)
    f = base / "drums" / "a.mid"
    f.write_bytes(b"data")
    g = base / "drums" / "sub" / "b.mid"
    g.write_bytes(b"data")
    (mirror / "drums" / "a.mid").write_bytes(b"data")
    (mirror / "drums" / "sub" / "b.mid").write_bytes(b"data")
    outside = base / "root.mid"
    outside.write_bytes(b"data")
    files = [
        {"name": "drums/a.mid", "path": str(f), "size": 4},
        {"name": "drums/sub/b.mid", "path": str(g), "size": 4},
        {"name": "root.mid", "path": str(outside), "size": 4},
    ]
    history = []

    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: mirror)
    monkeypatch.setattr(project_manager, "project_dir", lambda pid_: tmp_path)

    with patch.object(chat_service, "_persist_project_state"):
        updated, undo_stack, error = chat_service._on_delete_folder(
            pid, "drums", files, [], history,
        )
    assert error == ""
    assert [f["name"] for f in updated] == ["root.mid"]
    assert not (base / "drums").exists()
    assert not (mirror / "drums").exists()
    assert (tmp_path / ".trash" / "drums" / "a.mid").is_file()
    assert (tmp_path / ".trash" / "drums" / "sub" / "b.mid").is_file()
    # mkdirs 记录全部子目录（含空目录），撤销时逐级重建
    assert undo_stack[0]["mkdirs"] == ["drums", "drums/sub"]

    # 撤销：文件移回 + 目录层级重建
    with patch.object(chat_service, "_persist_project_state"):
        restored, _, _ = chat_service._on_undo(undo_stack, updated, pid, history)
    assert restored == files
    assert (base / "drums" / "a.mid").is_file()
    assert (base / "drums" / "sub" / "b.mid").is_file()
    assert (base / "drums" / "sub").is_dir()


def test_delete_folder_locked_file_aborts(tmp_path, monkeypatch):
    """文件夹内文件被占用移不进回收站 → 整体中止，文件夹与清单不变。"""
    import project_manager

    pid = "pid-dfolder-2"
    base = tmp_path / "base"
    (base / "drums").mkdir(parents=True)
    f = base / "drums" / "a.mid"
    f.write_bytes(b"data")
    files = [{"name": "drums/a.mid", "path": str(f), "size": 4}]
    history = []

    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: None)
    monkeypatch.setattr(chat_service, "_clear_trash", lambda pid_: None)

    with patch.object(chat_service, "_persist_project_state") as persist, \
         patch.object(chat_service, "_move_to_trash", return_value=None):
        updated, undo_stack, error = chat_service._on_delete_folder(
            pid, "drums", files, [], history,
        )
    assert error
    assert updated == files
    assert undo_stack == []
    assert (base / "drums" / "a.mid").is_file()
    persist.assert_not_called()


def test_open_workspace_creates_dir_and_startfile(tmp_path, monkeypatch):
    """打开工作区：目录不存在先创建，再调 os.startfile 打开。"""
    import project_manager

    pid = "pid-open-1"
    base = tmp_path / "base" / "nested"
    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    opened = []
    monkeypatch.setattr(chat_service.os, "startfile", lambda p: opened.append(p))

    result = chat_service.open_workspace(pid)
    assert result == {"ok": True, "path": str(base)}
    assert base.is_dir()
    assert opened == [str(base)]


def test_upload_uses_original_filename(tmp_path, monkeypatch):
    """上传用原始文件名而非 mkstemp 临时名。"""
    import project_manager

    pid = "pid-upload-1"
    base = tmp_path / "base"
    base.mkdir()
    # 模拟 server 端 mkstemp 临时文件
    tmp_file = tmp_path / "tmpabc123.mid"
    tmp_file.write_bytes(b"MThd\x00\x00\x00\x06" + b"\x00" * 10)
    monkeypatch.setattr(project_manager, "get_midi_base_dir", lambda pid_: base)
    monkeypatch.setattr(project_manager, "get_midi_mirror_dir", lambda pid_: None)
    monkeypatch.setattr(chat_service.get, "get_note", lambda p, save_to_file=False: [])

    with patch.object(chat_service, "_persist_project_state"):
        updated, undo_stack, _ = chat_service._on_upload(
            [str(tmp_file)], [], [], pid, [], names=["我的旋律.mid"],
        )
    assert updated[0]["name"] == "我的旋律.mid"
    assert (base / "我的旋律.mid").is_file()
    assert undo_stack[0]["files"] == []
