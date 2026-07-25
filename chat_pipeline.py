"""Pipeline helpers for send_message — extracted to keep send_message thin.

Each helper handles one phase of the message → AI → tool → reply pipeline.
"""
from __future__ import annotations

import json
import logging

import gradio as gr

import ai_api
import config

# Module reference for runtime attribute access (avoids circular import issues
# with mutable globals like _current_project_id).
import chat_ui as _chat_ui

logger = logging.getLogger("ai_midi")


# ════════════════════════════════════════════════════════════════════
# Phase 1: Context preparation
# ════════════════════════════════════════════════════════════════════

def _prepare_context(
    message: str,
    full_history: list[dict],
    midi_files: list[dict],
    settings: dict,
    history: list[dict],
) -> dict:
    """Build messages, API kwargs, client, and initial mutable state.

    Returns a context dict consumed by _execute_tool_loop.
    """
    openai_tools = _chat_ui._mcp_list_tools()
    logger.info("MCP 工具数量: %d", len(openai_tools))
    if openai_tools:
        logger.info("MCP 工具列表: %s", [t["function"]["name"] for t in openai_tools])
    else:
        logger.warning("MCP 工具列表为空！tools 参数不会传给 API")

    system_prompt = _chat_ui._build_system_prompt(midi_files)
    messages: list[dict] = [{"role": "system", "content": system_prompt}]
    for msg in full_history:
        messages.append(msg)
    messages.append({"role": "user", "content": message})
    _round_start_idx = len(messages)

    client = ai_api.get_client(
        api_key=settings["api_key"],
        base_url=settings["base_url"],
    )

    kwargs: dict = {
        "model": settings["model"],
        "messages": messages,
        "stream": False,
    }
    if openai_tools:
        kwargs["tools"] = openai_tools
    if settings["max_tokens"]:
        kwargs["max_tokens"] = int(settings["max_tokens"])
    if settings["max_completion_tokens"]:
        kwargs["max_completion_tokens"] = int(settings["max_completion_tokens"])
    if settings["reasoning_effort"]:
        kwargs["reasoning_effort"] = settings["reasoning_effort"]
    if settings["thinking_enabled"]:
        kwargs["extra_body"] = {"thinking": {"type": "enabled"}}

    updated_files = list(midi_files)
    download_path = None
    tool_log: list[str] = []
    max_tool_rounds = config.MAX_TOOL_ROUNDS
    chat_display = list(history)
    chat_display.append({"role": "user", "content": message})

    return {
        "kwargs": kwargs,
        "client": client,
        "messages": messages,
        "system_prompt": system_prompt,
        "_round_start_idx": _round_start_idx,
        "chat_display": chat_display,
        "updated_files": updated_files,
        "download_path": download_path,
        "tool_log": tool_log,
        "max_tool_rounds": max_tool_rounds,
    }


# ════════════════════════════════════════════════════════════════════
# Phase 2: Core tool-calling loop (generator)
# ════════════════════════════════════════════════════════════════════

def _execute_tool_loop(
    ctx: dict,
    new_undo_stack: list,
    full_history: list[dict],
    message: str,
):
    """Core multi-round tool-calling loop. Generator yielding intermediate states.

    Handles:
    - Context compression
    - API calls
    - Tool execution with display updates
    - Final reply streaming (delegates to _stream_final_reply)
    - Max-rounds fallback
    - Error handling
    """
    kwargs: dict = ctx["kwargs"]
    client = ctx["client"]
    messages: list[dict] = ctx["messages"]
    system_prompt: str = ctx["system_prompt"]
    _round_start_idx: int = ctx["_round_start_idx"]
    chat_display: list[dict] = ctx["chat_display"]
    updated_files: list[dict] = ctx["updated_files"]
    download_path: str | None = ctx["download_path"]
    tool_log: list[str] = ctx["tool_log"]
    max_tool_rounds: int = ctx["max_tool_rounds"]

    try:
        for round_idx in range(max_tool_rounds):
            # ── 上下文压缩 ──
            if _chat_ui._should_compact(messages, full_history):
                logger.warning(
                    "上下文过大 (约 %d tokens)，执行压缩...",
                    sum(len(str(m.get("content", ""))) for m in messages) // 3,
                )
                full_history = _chat_ui._compact_full_history(full_history)
                messages = [{"role": "system", "content": system_prompt}]
                for msg in full_history:
                    messages.append(msg)
                messages.append({"role": "user", "content": message})
                kwargs["messages"] = messages
                _round_start_idx = len(messages)
                logger.info("压缩完成，full_history 长度: %d", len(full_history))

            # 更新 system prompt 以反映当前文件列表
            kwargs["messages"][0] = {
                "role": "system",
                "content": _chat_ui._build_system_prompt(updated_files),
            }

            response = client.chat.completions.create(**kwargs)
            choice = response.choices[0]
            assistant_msg = choice.message

            logger.info(
                "API 返回: finish_reason=%s, tool_calls=%d, content=%s",
                choice.finish_reason,
                len(getattr(assistant_msg, "tool_calls", None) or []),
                (assistant_msg.content or "")[:100],
            )

            # ── 构造助手消息 ──
            msg_content = assistant_msg.content if assistant_msg.content else None
            msg_tool_calls = getattr(assistant_msg, "tool_calls", None) or []

            assistant_message: dict = {
                "role": "assistant",
                "content": msg_content,
            }
            if msg_tool_calls:
                assistant_message["tool_calls"] = [
                    {
                        "id": tc.id,
                        "type": tc.type or "function",
                        "function": {
                            "name": tc.function.name,
                            "arguments": tc.function.arguments,
                        },
                    }
                    for tc in msg_tool_calls
                ]

            messages.append(assistant_message)

            # ── 无工具调用 → 最终回复 ──
            if not msg_tool_calls:
                new_full_history = _finalize_response(
                    messages, full_history, message, _round_start_idx, updated_files,
                )
                final_display = msg_content or ""
                yield from _stream_final_reply(
                    chat_display, final_display, updated_files,
                    new_undo_stack, new_full_history, download_path,
                )
                return

            # ── 工具调用前说明文字 ──
            if msg_content and msg_content.strip():
                chat_display.append({"role": "assistant", "content": msg_content})
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    gr.update(choices=_chat_ui._get_choices(updated_files), value=[]),
                )

            # ── 执行工具调用 ──
            for tc in msg_tool_calls:
                tc_id = tc.id
                tc_name = tc.function.name
                tc_args_raw = tc.function.arguments

                # 显示"正在调用工具"状态
                chat_display.append({
                    "role": "assistant",
                    "content": f"🔧 正在调用 `{tc_name}`...",
                })
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    gr.update(choices=_chat_ui._get_choices(updated_files), value=[]),
                )

                try:
                    tc_args = (
                        json.loads(tc_args_raw)
                        if isinstance(tc_args_raw, str)
                        else tc_args_raw
                    )
                except json.JSONDecodeError:
                    tc_args = {}

                result_text, updated_files = _chat_ui._execute_tool_call(
                    {"function": {"name": tc_name, "arguments": tc_args}},
                    updated_files,
                )

                entry = _chat_ui._format_single_tool_entry(tc_name, tc_args, result_text)
                tool_log.append(entry)

                # 如果创建了文件，设置下载路径
                if tc_name == "create_midi":
                    filename = tc_args.get("filename", "")
                    try:
                        filepath = _chat_ui._resolve_created_midi_path(filename)
                    except ValueError as exc:
                        logger.warning("create_midi 下载路径无效: %s", exc)
                        filepath = None
                    if filepath is not None and filepath.is_file():
                        download_path = str(filepath)

                # 将工具结果回传给 API
                messages.append(
                    _chat_ui._make_tool_result_message(tc_id, result_text),
                )

                # 更新 chat_display：替换"正在调用..."
                if chat_display and chat_display[-1].get("role") == "assistant":
                    chat_display[-1] = {"role": "assistant", "content": entry}
                else:
                    chat_display.append({"role": "assistant", "content": entry})
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    gr.update(choices=_chat_ui._get_choices(updated_files), value=[]),
                )

        # ── 达到最大轮数 ──
        final_content = (
            f"⚠ 已达到最大工具调用轮数（{max_tool_rounds}），"
            "本轮已停止。请缩小任务范围后重试。"
        )
        messages.append({"role": "assistant", "content": final_content})

        new_full_history = _finalize_response(
            messages, full_history, message, _round_start_idx, updated_files,
        )

        full_display = final_content
        yield from _stream_final_reply(
            chat_display, full_display, updated_files,
            new_undo_stack, new_full_history, download_path,
        )

    # intentional: broad catch for API/MCP/file errors in user-facing generator
    except Exception as e:
        logger.exception("send_message 调用失败")
        if isinstance(e, GeneratorExit):
            raise
        tool_log_html = _chat_ui._format_tool_log(tool_log)
        # 不直接 str(e) 暴露给 UI，避免 API 异常可能包含敏感请求信息
        error_base = (
            f"⚠ 调用失败: {type(e).__name__}"
            + (f" (HTTP {e.status_code})" if hasattr(e, "status_code") else "")
        )
        error_msg = (
            (error_base + "\n\n" + tool_log_html).strip()
            if tool_log_html
            else error_base
        )
        chat_display.append({"role": "assistant", "content": error_msg})
        new_error_history = full_history + [{"role": "user", "content": message}]
        for api_msg in messages[_round_start_idx:]:
            if api_msg.get("role") in ("assistant", "tool"):
                new_error_history.append(api_msg)
        if _chat_ui._current_project_id:
            project_manager.save_history(
                _chat_ui._current_project_id, new_error_history, updated_files,
            )
        yield (
            list(chat_display),
            "",
            updated_files,
            new_undo_stack,
            new_error_history,
            None,
            gr.update(),
        )


# ════════════════════════════════════════════════════════════════════
# Phase 3: Final reply streaming
# ════════════════════════════════════════════════════════════════════

def _stream_final_reply(
    chat_display: list[dict],
    final_content: str,
    updated_files: list[dict],
    new_undo_stack: list,
    new_full_history: list[dict],
    download_path: str | None,
):
    """Character-by-character streaming display + history finalization.

    Yields updated states for each character.
    Calls _finalize_response internally for the save.
    """
    displayed = ""
    chat_display.append({"role": "assistant", "content": ""})
    for ch in final_content:
        displayed += ch
        chat_display[-1] = {"role": "assistant", "content": displayed}
        yield (
            list(chat_display),
            "",
            updated_files,
            new_undo_stack,
            new_full_history,
            download_path,
            gr.update(choices=_chat_ui._get_choices(updated_files), value=[]),
        )


# ════════════════════════════════════════════════════════════════════
# Phase 4: Response finalization (history build + save)
# ════════════════════════════════════════════════════════════════════

def _finalize_response(
    messages: list[dict],
    full_history: list[dict],
    message: str,
    _round_start_idx: int,
    updated_files: list[dict],
) -> list[dict]:
    """Build new_full_history from messages and save to project manager.

    Returns the new full history list.
    """
    new_full_history = full_history + [{"role": "user", "content": message}]
    for api_msg in messages[_round_start_idx:]:
        if api_msg.get("role") in ("assistant", "tool"):
            new_full_history.append(api_msg)

    if _chat_ui._current_project_id:
        project_manager.save_history(
            _chat_ui._current_project_id, new_full_history, updated_files,
        )

    return new_full_history
