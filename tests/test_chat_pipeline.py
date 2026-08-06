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


def _response(*, content=None, tool_call=None, reasoning=None):
    message = SimpleNamespace(
        content=content,
        tool_calls=[tool_call] if tool_call else [],
        reasoning_content=reasoning,
    )
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


def test_reasoning_content_persisted_in_final_history():
    ctx = _context([_response(content="最终回答", reasoning="思考过程内容")])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), patch.object(
        chat_pipeline._chat_ui, "_build_system_prompt", return_value="system",
    ), patch.object(
        chat_pipeline._chat_ui, "_should_compact", return_value=False,
    ):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    # 展示中包含可折叠思考过程
    assert "<summary>思考过程</summary>" in outputs[-1][0][-1]["content"]
    assert "思考过程内容" in outputs[-1][0][-1]["content"]
    # 落盘历史中 assistant 消息携带推理过程，重开对话后可重建展示
    final_msg = outputs[-1][4][-1]
    assert final_msg["reasoning_content"] == "思考过程内容"
    assert final_msg["content"] == "最终回答"


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


def test_sanitize_strips_reasoning_content_before_api_send():
    raw = [
        {"role": "assistant", "content": "回答", "reasoning_content": "思考中……"},
    ]

    sanitized = chat_pipeline._sanitize_messages(raw)

    assert "reasoning_content" not in sanitized[0]
    # 原消息不被修改（持久化仍保留推理过程）
    assert raw[0]["reasoning_content"] == "思考中……"


def test_format_display_message_with_reasoning():
    msg = chat_pipeline._format_display_message("思考如何和弦配理...", "和弦数据如下:")
    assert "<details>" in msg
    assert "<summary>思考过程</summary>" in msg
    assert "思考如何和弦配理..." in msg
    assert "和弦数据如下:" in msg

    msg_think_tag = chat_pipeline._format_display_message("", "<think>内置思考内容</think>最终回复")
    assert "<details>" in msg_think_tag
    assert "内置思考内容" in msg_think_tag
    assert "最终回复" in msg_think_tag


def test_format_display_message_merges_reasoning_and_think_blocks():
    """reasoning 与 content 内嵌 think 分别成块（不同思考不合并到一个块）。"""
    msg = chat_pipeline._format_display_message("推理A", "<think>推理B</think>正文")
    assert msg.count("<summary>思考过程</summary>") == 2
    assert "推理A" in msg
    assert "推理B" in msg
    assert "<think>" not in msg
    assert msg.rstrip().endswith("正文")


def test_format_display_message_handles_unclosed_think_tag():
    """未闭合的 <think> 兜底剥离标签，内容并入思考区。"""
    msg = chat_pipeline._format_display_message("", "前半<think>未闭合思考")
    assert "<think>" not in msg
    assert "未闭合思考" in msg
    assert "前半" in msg


def test_format_display_message_handles_multiple_think_blocks():
    msg = chat_pipeline._format_display_message("", "a<think>一</think>b<think>二</think>c")
    assert "一" in msg and "二" in msg
    assert "<think>" not in msg
    assert msg.rstrip().endswith("c")


def test_format_display_message_does_not_duplicate_same_thinking():
    """think 片段与 reasoning 相同 → 不重复拼接。"""
    msg = chat_pipeline._format_display_message("思考内容", "<think>思考内容</think>正文")
    assert msg.count("思考内容") == 1


def test_split_content_blocks():
    """多段 content 结构：thinking 块归推理，text 块归正文。"""
    assert chat_pipeline._split_content_blocks("plain") == ("", "plain")
    assert chat_pipeline._split_content_blocks(None) == ("", "")
    assert chat_pipeline._split_content_blocks(123) == ("", "123")

    reasoning, content = chat_pipeline._split_content_blocks([
        {"type": "thinking", "thinking": "T1"},
        {"type": "text", "text": "X1"},
        {"type": "reasoning", "reasoning": "T2"},
        "X2",
        {"type": "text"},  # 无 text 字段的块跳过
    ])
    assert reasoning == "T1T2"
    assert content == "X1X2"


def test_sanitize_messages_non_gemini_strips_extra_content():
    """非 Gemini 服务商：剥除 tool_calls 上的 extra_content，避免未知字段 400。"""
    raw = [
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [
                {
                    "id": "c1",
                    "type": "function",
                    "function": {"name": "read_library_file", "arguments": "{}"},
                    "extra_content": {"google": {"thought_signature": "SIG"}},
                }
            ],
        },
    ]
    sanitized = chat_pipeline._sanitize_messages(raw, is_gemini=False)
    assert "extra_content" not in sanitized[0]["tool_calls"][0]


def test_sanitize_messages_gemini_preserves_extra_content():
    raw = [
        {
            "role": "assistant",
            "content": "",
            "tool_calls": [
                {
                    "id": "c1",
                    "type": "function",
                    "function": {"name": "read_library_file", "arguments": "{}"},
                    "extra_content": {"google": {"thought_signature": "REAL_SIG"}},
                }
            ],
        },
    ]
    sanitized = chat_pipeline._sanitize_messages(raw, is_gemini=True)
    assert sanitized[0]["tool_calls"][0]["extra_content"]["google"]["thought_signature"] == "REAL_SIG"


def test_non_gemini_tool_calls_have_no_extra_content():
    """非 Gemini 项目：落盘历史中的 tool_call 不带 extra_content。"""
    ctx = _context([_response(tool_call=_tool_call("song.mid")), _response(content="done")])
    ctx["is_gemini"] = False
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), patch.object(
        chat_pipeline._chat_ui, "_build_system_prompt", return_value="system",
    ), patch.object(
        chat_pipeline._chat_ui, "_should_compact", return_value=False,
    ), patch.object(
        chat_pipeline._chat_ui, "_execute_tool_call", return_value=("ok", []),
    ), patch.object(
        chat_pipeline._chat_ui, "_resolve_created_midi_path",
        return_value=__import__("pathlib").Path("missing.mid"),
    ):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    assistant_msgs = [
        msg for msg in outputs[-1][4]
        if msg.get("role") == "assistant" and "tool_calls" in msg
    ]
    assert len(assistant_msgs) == 1
    assert "extra_content" not in assistant_msgs[0]["tool_calls"][0]


def test_streaming_chunk_content_list_blocks_dont_crash():
    """部分模型 content 为多段结构：thinking 块归入推理、text 块归入正文，不崩溃。"""
    chunks = [
        SimpleNamespace(choices=[SimpleNamespace(delta=SimpleNamespace(
            content=[
                {"type": "thinking", "thinking": "推理片段"},
                {"type": "text", "text": "正文片段"},
            ]
        ))]),
    ]
    ctx = _context([chunks, [_response(content="done")]])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), patch.object(
        chat_pipeline._chat_ui, "_build_system_prompt", return_value="system",
    ), patch.object(
        chat_pipeline._chat_ui, "_should_compact", return_value=False,
    ):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    last = outputs[-1][0][-1]
    assert "<summary>思考过程</summary>" in last["content"]
    assert "推理片段" in last["content"]
    assert "正文片段" in last["content"]


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


def test_stream_disconnect_retries_and_rolls_back_display():
    """上游流式中途断连（RemoteProtocolError）→ 自动重试，断连轮次内容回滚。

    回归：重试范围必须覆盖流式迭代——连接 200 后 chunked 传输被掐断
    时，丢弃已生成/已展示的内容重新请求，而非直接报错。
    """
    import httpx

    class _BrokenStream:
        """create() 返回后迭代中途抛 RemoteProtocolError（模拟网关掐断流）。"""

        def __iter__(self):
            yield _streaming_chunk(content="部分内容")
            raise httpx.RemoteProtocolError(
                "peer closed connection without sending complete message body (incomplete chunked read)"
            )

    ctx = _context([_BrokenStream(), [_response(content="重试成功")]])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), \
         patch.object(chat_pipeline._chat_ui, "_build_system_prompt", return_value="system"), \
         patch.object(chat_pipeline._chat_ui, "_should_compact", return_value=False), \
         patch.object(chat_pipeline.time, "sleep") as sleep_mock:
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    # 断连轮次的残留内容被回滚，不进入最终展示与落盘历史
    final_display = outputs[-1][0]
    assert not any("部分内容" in m.get("content", "") for m in final_display)
    assert final_display[-1]["content"] == "重试成功"
    assert outputs[-1][4][-1] == {"role": "assistant", "content": "重试成功"}
    # 重试确实等待了 5 秒间隔（time.sleep 被调用）
    sleep_mock.assert_called()


def test_stream_tool_calls_read_from_message_when_delta_empty():
    """末 chunk 把 tool_calls 放在 message、delta 为非空空对象时，工具调用不丢失。"""
    chunks = [
        SimpleNamespace(
            choices=[SimpleNamespace(
                delta=SimpleNamespace(),  # 非 None 的空 delta（OpenAI 兼容端点常见）
                message=SimpleNamespace(
                    tool_calls=[
                        SimpleNamespace(
                            index=0,
                            id="call-1",
                            function=SimpleNamespace(
                                name="read_library_file",
                                arguments='{"filename": "01.md"}',
                            ),
                        )
                    ],
                ),
            )],
        ),
        [_response(content="done")],
    ]
    ctx = _context(chunks)
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), \
         patch.object(chat_pipeline._chat_ui, "_build_system_prompt", return_value="system"), \
         patch.object(chat_pipeline._chat_ui, "_should_compact", return_value=False), \
         patch.object(chat_pipeline._chat_ui, "_execute_tool_call", return_value=("文件内容", [])):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    assistant_msgs = [
        msg for msg in outputs[-1][4]
        if msg.get("role") == "assistant" and "tool_calls" in msg
    ]
    assert len(assistant_msgs) == 1
    assert assistant_msgs[0]["tool_calls"][0]["function"]["name"] == "read_library_file"


def test_tool_execution_error_produces_failure_block():
    """工具执行抛错时生成失败结果块，而非悬空的「正在调用」占位符。"""
    ctx = _context([_response(tool_call=_tool_call("song.mid")), _response(content="done")])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), \
         patch.object(chat_pipeline._chat_ui, "_build_system_prompt", return_value="system"), \
         patch.object(chat_pipeline._chat_ui, "_should_compact", return_value=False), \
         patch.object(chat_pipeline._chat_ui, "_execute_tool_call",
                      side_effect=RuntimeError("boom")), \
         patch.object(chat_pipeline._chat_ui, "_resolve_created_midi_path",
                      return_value=__import__("pathlib").Path("missing.mid")):
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    display = outputs[-1][0]
    tool_blocks = [m for m in display if "🔧 调用" in m.get("content", "")]
    assert len(tool_blocks) == 1
    assert "错误：工具执行失败" in tool_blocks[0]["content"]


def test_stream_non_transient_error_no_retry():
    """流式迭代中的非瞬态错误不触发重试，转为「调用失败」提示（原有行为）。"""
    class _BrokenStream:
        def __iter__(self):
            raise ValueError("本地错误")

    ctx = _context([_BrokenStream()])
    with patch.object(chat_pipeline._chat_ui, "_current_project_id", None), \
         patch.object(chat_pipeline._chat_ui, "_build_system_prompt", return_value="system"), \
         patch.object(chat_pipeline._chat_ui, "_should_compact", return_value=False), \
         patch.object(chat_pipeline.time, "sleep") as sleep_mock:
        outputs = list(chat_pipeline._execute_tool_loop(ctx, [], [], "go"))

    assert "⚠ 调用失败" in outputs[-1][0][-1]["content"]
    sleep_mock.assert_not_called()


def test_execute_tool_call_create_midi_uses_relpath(tmp_path, monkeypatch):
    """create_midi 后 file_info.name 用相对主目录的路径（含子目录）。"""
    import chat_service

    base = tmp_path / "base"
    (base / "drums").mkdir(parents=True)
    created = base / "drums" / "beat.mid"
    created.write_bytes(b"MThd\x00\x00\x00\x06")

    monkeypatch.setattr(chat_service, "_current_project_id", "pid1")
    monkeypatch.setattr(chat_service.project_manager, "get_midi_base_dir", lambda pid: base)
    monkeypatch.setattr(chat_service, "_resolve_created_midi_path", lambda fn: created)
    monkeypatch.setattr(chat_service, "_mcp_call_tool", lambda name, args: "ok")
    monkeypatch.setattr(chat_service.get, "get_note", lambda p, save_to_file=False: ['[note: "C4"]'])

    tool_call = {"function": {"name": "create_midi", "arguments": {"filename": "drums/beat.mid"}}}
    result, files = chat_service._execute_tool_call(tool_call, [])
    assert result == "ok"
    assert len(files) == 1
    assert files[0]["name"] == "drums/beat.mid"
    assert files[0]["path"] == str(created)


def test_execute_tool_call_delete_midi_matches_relpath(monkeypatch):
    """delete_midi 按 relpath 匹配（含子目录）。"""
    import chat_service

    existing = [{"name": "drums/beat.mid", "path": "/x/drums/beat.mid", "size": 10, "note_table": ""}]
    monkeypatch.setattr(chat_service, "_mcp_call_tool", lambda name, args: "deleted")

    tool_call = {"function": {"name": "delete_midi", "arguments": {"filename": "drums/beat.mid"}}}
    _, files = chat_service._execute_tool_call(tool_call, existing)
    assert files == []


def test_execute_tool_call_delete_midi_matches_basename(monkeypatch):
    """delete_midi 兼容裸文件名匹配（旧历史 name 可能是裸名）。"""
    import chat_service

    existing = [{"name": "drums/beat.mid", "path": "/x/drums/beat.mid", "size": 10, "note_table": ""}]
    monkeypatch.setattr(chat_service, "_mcp_call_tool", lambda name, args: "deleted")

    tool_call = {"function": {"name": "delete_midi", "arguments": {"filename": "beat.mid"}}}
    _, files = chat_service._execute_tool_call(tool_call, existing)
    assert files == []


def test_execute_tool_call_parse_midi_matches_relpath(monkeypatch):
    """parse_midi 按 relpath 匹配并更新 note_table。"""
    import chat_service

    existing = [{"name": "sub/x.mid", "path": "/x/sub/x.mid", "size": 10, "note_table": ""}]
    monkeypatch.setattr(chat_service, "_mcp_call_tool", lambda name, args: "parsed_notes")

    tool_call = {"function": {"name": "parse_midi", "arguments": {"filename": "sub/x.mid"}}}
    _, files = chat_service._execute_tool_call(tool_call, existing)
    assert files[0]["note_table"] == "parsed_notes"



