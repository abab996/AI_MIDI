from __future__ import annotations

import json
from types import SimpleNamespace
from unittest.mock import patch

import chat_pipeline


class _Completions:
    def __init__(self, responses):
        self._responses = iter(responses)

    def create(self, **_kwargs):
        return next(self._responses)


def _response(*, content=None, tool_call=None):
    message = SimpleNamespace(content=content, tool_calls=[tool_call] if tool_call else [])
    return SimpleNamespace(
        choices=[SimpleNamespace(finish_reason="tool_calls" if tool_call else "stop", message=message)],
    )


def _tool_call(filename="../escape.mid"):
    return SimpleNamespace(
        id="call-1",
        type="function",
        function=SimpleNamespace(
            name="create_midi",
            arguments=json.dumps({"filename": filename, "notes": "[]"}),
        ),
    )


def _context(responses, max_rounds=2):
    messages = [{"role": "system", "content": "system"}, {"role": "user", "content": "go"}]
    return {
        "kwargs": {"messages": messages},
        "client": SimpleNamespace(chat=SimpleNamespace(completions=_Completions(responses))),
        "messages": messages,
        "system_prompt": "system",
        "_round_start_idx": len(messages),
        "chat_display": [{"role": "user", "content": "go"}],
        "updated_files": [],
        "download_path": None,
        "tool_log": [],
        "max_tool_rounds": max_rounds,
    }


def test_invalid_create_filename_does_not_abort_tool_loop():
    ctx = _context([_response(tool_call=_tool_call()), _response(content="done")])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), patch.object(
        chat_pipeline._chat_ui, "_build_system_prompt", return_value="system",
    ), patch.object(
        chat_pipeline._chat_ui, "_should_compact", return_value=False,
    ), patch.object(
        chat_pipeline._chat_ui, "_execute_tool_call", return_value=("错误：非法文件名", []),
    ):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    assert outputs[-1][0][-1]["content"] == "done"
    assert outputs[-1][5] is None


def test_max_tool_rounds_returns_explicit_message():
    ctx = _context([_response(tool_call=_tool_call("song.mid"))], max_rounds=1)
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), patch.object(
        chat_pipeline._chat_ui, "_build_system_prompt", return_value="system",
    ), patch.object(
        chat_pipeline._chat_ui, "_should_compact", return_value=False,
    ), patch.object(
        chat_pipeline._chat_ui, "_execute_tool_call", return_value=("ok", []),
    ), patch.object(
        chat_pipeline._chat_ui, "_resolve_created_midi_path", return_value=__import__("pathlib").Path("missing.mid"),
    ):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    final_text = outputs[-1][0][-1]["content"]
    assert "已达到最大工具调用轮数" in final_text
    assert outputs[-1][4][-1] == {"role": "assistant", "content": final_text}
