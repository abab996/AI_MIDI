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


def test_sanitize_messages_fixes_none_content_and_missing_tool_name():
    raw_messages = [
        {"role": "user", "content": "hello"},
        {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "call-123",
                    "type": "function",
                    "function": {"name": "read_library_file", "arguments": "{}"},
                }
            ],
        },
        {
            "role": "tool",
            "tool_call_id": "call-123",
            "content": "file content",
        },
    ]

    sanitized = chat_pipeline._sanitize_messages(raw_messages)

    assert sanitized[1]["content"] == ""
    assert sanitized[2]["name"] == "read_library_file"


def test_format_display_message_with_reasoning():
    msg = chat_pipeline._format_display_message("思考如何和弦配理...", "和弦数据如下:")
    assert "<details>" in msg
    assert "<summary>🧠 思考过程</summary>" in msg
    assert "思考如何和弦配理..." in msg
    assert "和弦数据如下:" in msg

    msg_think_tag = chat_pipeline._format_display_message("", "<think>内置思考内容</think>最终回复")
    assert "<details>" in msg_think_tag
    assert "内置思考内容" in msg_think_tag
    assert "最终回复" in msg_think_tag


def test_streaming_duplicate_tool_name_deduplication():
    chunks = [
        SimpleNamespace(
            choices=[
                SimpleNamespace(
                    delta=SimpleNamespace(
                        tool_calls=[
                            SimpleNamespace(
                                index=0,
                                id="call_gemini_1",
                                function=SimpleNamespace(
                                    name="read_library_file",
                                    arguments='{"filename": "02_配和弦指南.md"}',
                                ),
                            )
                        ]
                    )
                )
            ]
        ),
        SimpleNamespace(
            choices=[
                SimpleNamespace(
                    delta=SimpleNamespace(
                        tool_calls=[
                            SimpleNamespace(
                                index=0,
                                id="call_gemini_1",
                                function=SimpleNamespace(
                                    name="read_library_file",
                                    arguments='{"filename": "02_配和弦指南.md"}',
                                ),
                            )
                        ]
                    )
                )
            ]
        ),
        SimpleNamespace(
            choices=[
                SimpleNamespace(
                    delta=SimpleNamespace(content="文件读取完成")
                )
            ]
        ),
    ]

    ctx = _context([chunks, [_response(content="done")]])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), patch.object(
        chat_pipeline._chat_ui, "_build_system_prompt", return_value="system",
    ), patch.object(
        chat_pipeline._chat_ui, "_should_compact", return_value=False,
    ), patch.object(
        chat_pipeline._chat_ui, "_execute_tool_call", return_value=("文件内容", []),
    ):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    assistant_msgs_with_tools = [
        msg for msg in outputs[-1][4]
        if msg.get("role") == "assistant" and "tool_calls" in msg
    ]
    assert len(assistant_msgs_with_tools) == 1
    assert assistant_msgs_with_tools[0]["tool_calls"][0]["function"]["name"] == "read_library_file"


def _streaming_chunk(*, content=None, tool_call=None):
    """构造一个流式响应 chunk（SimpleNamespace，模拟 openai SDK 流式对象）。"""
    if content is not None:
        delta = SimpleNamespace(content=content)
    elif tool_call is not None:
        delta = SimpleNamespace(tool_calls=[tool_call])
    else:
        delta = SimpleNamespace()
    return SimpleNamespace(choices=[SimpleNamespace(delta=delta)])


def _delta_tool_call(*, name="read_library_file", args='{"filename": "01.md"}', signature=None):
    """构造一个流式 tool_call delta；signature 非 None 时附带 extra_content.google.thought_signature。"""
    tc = SimpleNamespace(
        index=0,
        id="call_gemini_1",
        function=SimpleNamespace(name=name, arguments=args),
    )
    if signature is not None:
        tc.extra_content = {"google": {"thought_signature": signature}}
    return tc


def _run_tool_loop_with_chunks(chunks):
    ctx = _context([chunks, [_response(content="done")]])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), patch.object(
        chat_pipeline._chat_ui, "_build_system_prompt", return_value="system",
    ), patch.object(
        chat_pipeline._chat_ui, "_should_compact", return_value=False,
    ), patch.object(
        chat_pipeline._chat_ui, "_execute_tool_call", return_value=("文件内容", []),
    ):
        return list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))


def _assistant_tool_calls_from_history(outputs):
    return [
        msg for msg in outputs[-1][4]
        if msg.get("role") == "assistant" and "tool_calls" in msg
    ]


def test_extract_thought_signature_from_dict_and_object():
    # dict 形式（OpenAI 兼容端点原始 JSON 解析后）
    assert chat_pipeline._extract_thought_signature(
        {"extra_content": {"google": {"thought_signature": "SIG_DICT"}}}
    ) == "SIG_DICT"
    # pydantic 对象形式（SDK extra="allow" 下字段作为属性）
    assert chat_pipeline._extract_thought_signature(
        SimpleNamespace(extra_content={"google": {"thought_signature": "SIG_OBJ"}})
    ) == "SIG_OBJ"
    # 缺失时返回空串
    assert chat_pipeline._extract_thought_signature({}) == ""
    assert chat_pipeline._extract_thought_signature(SimpleNamespace()) == ""
    assert chat_pipeline._extract_thought_signature({"extra_content": {}}) == ""
    assert chat_pipeline._extract_thought_signature({"extra_content": {"google": {}}}) == ""


def test_streaming_captures_and_round_trips_thought_signature():
    sig = "ErUECrIEAXLI2nxC8=="
    chunks = [
        _streaming_chunk(tool_call=_delta_tool_call(signature=sig)),
        _streaming_chunk(content="读取完成"),
    ]
    outputs = _run_tool_loop_with_chunks(chunks)

    assistant_msgs = _assistant_tool_calls_from_history(outputs)
    assert len(assistant_msgs) == 1
    tc = assistant_msgs[0]["tool_calls"][0]
    # 真实 signature 原样回传到 assistant 消息的 tool_call 上
    assert tc["extra_content"]["google"]["thought_signature"] == sig


def test_streaming_falls_back_to_validator_skip_when_no_signature():
    chunks = [
        _streaming_chunk(tool_call=_delta_tool_call()),  # 未携带 signature
        _streaming_chunk(content="读取完成"),
    ]
    outputs = _run_tool_loop_with_chunks(chunks)

    assistant_msgs = _assistant_tool_calls_from_history(outputs)
    assert len(assistant_msgs) == 1
    tc = assistant_msgs[0]["tool_calls"][0]
    # 无 signature 时用 bypass 串，避免 Gemini 400
    assert tc["extra_content"]["google"]["thought_signature"] == "skip_thought_signature_validator"


def test_sanitize_messages_backfills_missing_thought_signature():
    raw_messages = [
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [
                {
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "read_library_file", "arguments": "{}"},
                }
            ],
        },
    ]
    sanitized = chat_pipeline._sanitize_messages(raw_messages)
    tc = sanitized[0]["tool_calls"][0]
    assert tc["extra_content"]["google"]["thought_signature"] == "skip_thought_signature_validator"


def test_sanitize_messages_preserves_existing_thought_signature():
    raw_messages = [
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [
                {
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "read_library_file", "arguments": "{}"},
                    "extra_content": {"google": {"thought_signature": "REAL_SIG"}},
                }
            ],
        },
    ]
    sanitized = chat_pipeline._sanitize_messages(raw_messages)
    assert sanitized[0]["tool_calls"][0]["extra_content"]["google"]["thought_signature"] == "REAL_SIG"



