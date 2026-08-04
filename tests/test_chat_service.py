from __future__ import annotations

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

    with patch.object(chat_service, "_persist_project_state") as persist:
        updated, undo_stack, _ = chat_service._on_delete(
            files, ["B/b.mid"], [], "project-id", history,
        )

    assert updated == [files[0]]
    assert undo_stack == [files]
    persist.assert_called_once_with("project-id", history, updated)


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
