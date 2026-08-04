"""Pipeline helpers for send_message — extracted to keep send_message thin.

Each helper handles one phase of the message → AI → tool → reply pipeline.
"""
from __future__ import annotations

import json
import logging
import time

import httpx
import openai

import ai_api
import config
import project_manager

# Module reference for runtime attribute access (avoids circular import issues
# with mutable globals like _current_project_id).
import chat_service as _chat_ui

logger = logging.getLogger("ai_midi")

# 流式展示帧的节流间隔（秒）：格式化/全量拷贝成本高，与 chat_service 的
# SSE 节流保持一致；被节流吞掉的中间态由轮次结束前的强制 flush 兜底补发。
_STREAM_EMIT_INTERVAL = 0.025


def _sanitize_messages(messages: list[dict], *, is_gemini: bool = True) -> list[dict]:
    """清理并修复消息列表，确保兼容 Gemini 等 API 的严格要求。

    1. 将 assistant 消息中 content 为 None 改为 ""。
    2. 确保 tool 消息包含 name 字段（基于上下文 assistant tool_calls 补充）。
    3. Gemini 思考模型的 tool_call 需要 thought_signature（缺失时兜底补
       bypass 串）；非 Gemini 服务商不认识 extra_content 扩展字段，剥除避免 400。
    """
    tc_id_to_name: dict[str, str] = {}
    sanitized: list[dict] = []

    for msg in messages:
        msg_copy = dict(msg)
        role = msg_copy.get("role")

        if role == "assistant":
            if msg_copy.get("content") is None:
                msg_copy["content"] = ""
            # reasoning_content 仅用于前端展示与持久化，
            # 发送给 API 前剥除（部分服务商会拒绝未知字段）
            msg_copy.pop("reasoning_content", None)
            tool_calls = msg_copy.get("tool_calls") or []
            for tc in tool_calls:
                if isinstance(tc, dict):
                    tc_id = tc.get("id")
                    func = tc.get("function") or {}
                    tc_name = func.get("name") if isinstance(func, dict) else getattr(func, "name", None)
                    if is_gemini:
                        # 兜底：补上 Gemini 思考模型要求的 thought_signature（含修复前
                        # 保存的旧历史），缺签名时用 bypass 串让 Gemini 跳过校验，避免 400。
                        extra = tc.get("extra_content")
                        if not isinstance(extra, dict):
                            tc["extra_content"] = {
                                "google": {"thought_signature": "skip_thought_signature_validator"}
                            }
                        else:
                            g = extra.get("google")
                            if not (isinstance(g, dict) and g.get("thought_signature")):
                                extra["google"] = {"thought_signature": "skip_thought_signature_validator"}
                    else:
                        # 非 Gemini 服务商不认 extra_content 扩展字段，剥除避免 400
                        tc.pop("extra_content", None)
                else:
                    tc_id = getattr(tc, "id", None)
                    func = getattr(tc, "function", None)
                    tc_name = getattr(func, "name", None) if func else None
                if tc_id and tc_name:
                    tc_id_to_name[tc_id] = tc_name

        elif role == "tool":
            tc_id = msg_copy.get("tool_call_id")
            if not msg_copy.get("name") and tc_id in tc_id_to_name:
                msg_copy["name"] = tc_id_to_name[tc_id]

        sanitized.append(msg_copy)

    return sanitized


def _extract_thought_signature(tc_item) -> str:
    """从流式 tool_call delta 提取 Gemini 思考模型的 thought_signature。

    Gemini 思考模型通过 OpenAI 兼容端点做 function calling 时，会在每个
    tool_call 上附带 ``extra_content.google.thought_signature``。回传
    assistant 的 tool_calls 消息时必须原样带回，否则下一轮请求报 400
    "Function call is missing a thought_signature"。

    兼容 dict 与 pydantic 对象两种 ``tc_item``（SDK extra="allow" 下未建模
    字段保留在 extra_content）。
    """
    if isinstance(tc_item, dict):
        extra = tc_item.get("extra_content")
    else:
        extra = getattr(tc_item, "extra_content", None)
        if extra is None:
            model_extra = getattr(tc_item, "__pydantic_extra__", None)
            if isinstance(model_extra, dict):
                extra = model_extra.get("extra_content")
    if not isinstance(extra, dict):
        return ""
    google = extra.get("google")
    if not isinstance(google, dict):
        return ""
    sig = google.get("thought_signature")
    return sig if isinstance(sig, str) and sig else ""


def _extract_think_blocks(content: str) -> tuple[str, list[str]]:
    """从正文中提取所有 ``<think>...</think>`` 片段。

    返回 (去除标签后的正文, 思考片段列表)。支持：
    - 多个 think 块
    - 未闭合的 ``<think>``（其后的内容视为思考，兜底剥离标签）
    - 返回的正文中不再残留任何字面标签
    """
    answer_parts: list[str] = []
    think_parts: list[str] = []
    rest = content
    while True:
        start = rest.find("<think>")
        if start == -1:
            answer_parts.append(rest)
            break
        answer_parts.append(rest[:start])
        tail = rest[start + len("<think>"):]
        end = tail.find("</think>")
        if end == -1:
            think_parts.append(tail.strip())
            break
        think_parts.append(tail[:end].strip())
        rest = tail[end + len("</think>"):]
    return "".join(answer_parts), think_parts


def _split_content_blocks(value) -> tuple[str, str]:
    """把 chunk 的 content 字段拆分为 (推理, 正文) 两个字符串。

    字符串直接返回（推理为空）；多段结构（部分模型的 content 为
    ``[{"type": "thinking", "thinking": "..."}, {"type": "text", "text": "..."}]``）
    按块类型分流：thinking/reasoning 块归入推理，其余归入正文。
    """
    if value is None:
        return "", ""
    if isinstance(value, str):
        return "", value
    reasoning = ""
    content = ""
    if isinstance(value, (list, tuple)):
        for block in value:
            if isinstance(block, dict):
                btype = str(block.get("type", ""))
                if btype in ("thinking", "reasoning"):
                    text = (
                        block.get("thinking")
                        or block.get("reasoning")
                        or block.get("content")
                        or block.get("text")
                    )
                    if isinstance(text, str):
                        reasoning += text
                else:
                    text = block.get("text") or block.get("content") or block.get("output_text")
                    if isinstance(text, str):
                        content += text
            elif isinstance(block, str):
                content += block
    else:
        content = str(value)
    return reasoning, content


def _format_display_message(reasoning: str, content: str) -> str:
    """结合思考/推理过程和回答正文，构造前端 Markdown HTML 展示文本。

    - reasoning_content 与正文内嵌的 ``<think>`` 片段统一合并为单个可折叠思考块
    - 正文中不会残留任何字面 ``<think>`` 标签（多个/未闭合标签兜底处理）
    - 思考片段与 reasoning 相同（或为其长片段子串）时不重复拼接
    """

    def _clean(text: str) -> str:
        """去掉推理内容开头的 🧠 等装饰 emoji（部分模型会自行附加）。"""
        return text.strip().lstrip("\U0001F9E0").strip()

    reasoning = str(reasoning or "")
    content = str(content or "")

    answer, think_parts = _extract_think_blocks(content)

    merged = _clean(reasoning)
    for part in think_parts:
        part = _clean(part)
        if not part or part == merged:
            continue
        # 长片段整体已存在于 reasoning 中视为重复，避免同一段思考被渲染两遍
        if len(part) >= 20 and merged and part in merged:
            continue
        merged = f"{merged}\n\n{part}" if merged else part

    res = ""
    if merged:
        res += (
            f"<details>\n"
            f"<summary>思考过程</summary>\n\n"
            f"{merged}\n"
            f"</details>\n\n"
        )
    res += answer
    return res


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
    # 仅 Gemini 需要 thought_signature 扩展字段；其他服务商一律剥除
    is_gemini = config.is_gemini_provider(settings["base_url"])
    messages = _sanitize_messages(messages, is_gemini=is_gemini)
    _round_start_idx = len(messages)

    client = ai_api.get_client(
        api_key=settings["api_key"],
        base_url=settings["base_url"],
        timeout=config.CHAT_TIMEOUT_SECONDS,
    )

    kwargs: dict = {
        "model": settings["model"],
        "messages": messages,
        "stream": True,
    }
    if openai_tools:
        kwargs["tools"] = openai_tools
    if settings["max_tokens"]:
        kwargs["max_tokens"] = int(settings["max_tokens"])
    if settings["max_completion_tokens"]:
        kwargs["max_completion_tokens"] = int(settings["max_completion_tokens"])
    if settings["reasoning_effort"]:
        kwargs["reasoning_effort"] = settings["reasoning_effort"]
    # thinking 扩展参数仅 Gemini 的 OpenAI 兼容接口支持,其他服务商按标准 OpenAI 格式调用。
    if settings["thinking_enabled"] and config.is_gemini_provider(settings["base_url"]):
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
        "is_gemini": is_gemini,
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
    - API calls (real SSE streaming)
    - Reasoning/thinking process streaming and rendering
    - Tool execution with display updates
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
    # 仅 Gemini 服务商需要 thought_signature 扩展字段（测试构造的 ctx 无此键时按 Gemini 处理）
    is_gemini: bool = ctx.get("is_gemini", True)

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
            messages[0] = {
                "role": "system",
                "content": _chat_ui._build_system_prompt(updated_files),
            }
            kwargs["messages"] = _sanitize_messages(messages, is_gemini=is_gemini)
            kwargs["stream"] = True

            # 尝试 API 调用,逐步去除不兼容参数并重试
            _strippable = [
                ("extra_body", "thinking"),
                ("reasoning_effort", "reasoning_effort"),
            ]
            max_retries = 3
            for _attempt in range(max_retries + 1):
                try:
                    stream_response = client.chat.completions.create(**kwargs)
                    break
                except Exception as api_err:
                    is_rate_limit = (
                        getattr(api_err, "status_code", None) == 429
                        or "429" in str(api_err)
                        or "rate" in str(api_err).lower()
                        or "resource_exhausted" in str(api_err).lower()
                    )
                    if is_rate_limit:
                        err_kind = "API 限流"
                    elif isinstance(
                        api_err, (openai.APITimeoutError, httpx.TimeoutException)
                    ) or "timed out" in str(api_err).lower():
                        err_kind = "请求超时"
                    elif ai_api.is_upstream_transient(api_err):
                        err_kind = "上游服务繁忙"
                    else:
                        err_kind = None
                    # 上游瞬时故障：固定 5 秒间隔自动重试 3 次，3 次仍失败再返回错误码
                    if err_kind and _attempt < max_retries:
                        wait_sec = 5
                        logger.warning("触发 %s，等待 %d 秒后重试 (%d/%d)...", err_kind, wait_sec, _attempt + 1, max_retries)
                        time.sleep(wait_sec)
                        continue

                    stripped = False
                    for param_key, error_kw in _strippable:
                        if param_key in kwargs and error_kw in str(api_err).lower():
                            logger.warning("API 不支持 %s 参数,去掉后重试", param_key)
                            kwargs.pop(param_key)
                            stripped = True
                            break
                    if not stripped:
                        raise

            if not hasattr(stream_response, "__iter__") or isinstance(stream_response, dict):
                chunks = [stream_response]
            else:
                chunks = stream_response

            accumulated_content = ""
            accumulated_reasoning = ""
            tool_calls_builder: dict[int, dict] = {}
            is_streaming = False
            last_stream_emit = 0.0

            for chunk in chunks:
                if not hasattr(chunk, "choices") or not chunk.choices:
                    continue
                choice = chunk.choices[0]
                delta = getattr(choice, "delta", None)
                msg_obj = getattr(choice, "message", None)
                item = delta if delta is not None else msg_obj
                if item is None:
                    continue

                # 提取推理/思考片段（兼容字符串；非字符串兜底 str 化，避免 TypeError）
                r_chunk = (
                    getattr(item, "reasoning_content", None)
                    or getattr(item, "reasoning", None)
                )
                if r_chunk:
                    accumulated_reasoning += r_chunk if isinstance(r_chunk, str) else str(r_chunk)

                # 提取正文片段（多段结构时 thinking 块归入推理、text 块归入正文）
                c_chunk = getattr(item, "content", None)
                if c_chunk:
                    if isinstance(c_chunk, str):
                        accumulated_content += c_chunk
                    else:
                        c_reasoning, c_content = _split_content_blocks(c_chunk)
                        if c_reasoning:
                            accumulated_reasoning += c_reasoning
                        if c_content:
                            accumulated_content += c_content

                # 提取工具调用片段
                tc_chunks = getattr(item, "tool_calls", None)
                if tc_chunks:
                    for tc_item in tc_chunks:
                        idx = getattr(tc_item, "index", 0)
                        if idx not in tool_calls_builder:
                            tool_calls_builder[idx] = {
                                "id": "",
                                "type": "function",
                                "function": {"name": "", "arguments": ""},
                                "thought_signature": "",
                            }
                        tc_id = getattr(tc_item, "id", None)
                        if tc_id:
                            curr_id = tool_calls_builder[idx]["id"]
                            if not curr_id:
                                tool_calls_builder[idx]["id"] = tc_id
                            elif curr_id != tc_id and tc_id not in curr_id:
                                tool_calls_builder[idx]["id"] += tc_id

                        if isinstance(tc_item, dict):
                            func_obj = tc_item.get("function", {})
                            f_name = func_obj.get("name", "")
                            f_args = func_obj.get("arguments", "")
                        else:
                            func_obj = getattr(tc_item, "function", None)
                            f_name = getattr(func_obj, "name", "") if func_obj else ""
                            f_args = getattr(func_obj, "arguments", "") if func_obj else ""

                        if f_name:
                            curr_name = tool_calls_builder[idx]["function"]["name"]
                            if not curr_name:
                                tool_calls_builder[idx]["function"]["name"] = f_name
                            elif curr_name != f_name:
                                known_tools = [
                                    "read_library_file", "list_midi_files",
                                    "parse_midi", "create_midi", "delete_midi",
                                    "create_folder", "list_project_structure"
                                ]
                                if f_name in known_tools:
                                    tool_calls_builder[idx]["function"]["name"] = f_name
                                elif curr_name not in known_tools and f_name not in curr_name:
                                    tool_calls_builder[idx]["function"]["name"] += f_name

                        if f_args:
                            if isinstance(f_args, dict):
                                f_args = json.dumps(f_args, ensure_ascii=False)
                            curr_args = tool_calls_builder[idx]["function"]["arguments"]
                            if not curr_args:
                                tool_calls_builder[idx]["function"]["arguments"] = f_args
                            elif curr_args != f_args:
                                try:
                                    json.loads(curr_args)
                                    try:
                                        json.loads(f_args)
                                        if len(f_args) > len(curr_args):
                                            tool_calls_builder[idx]["function"]["arguments"] = f_args
                                    except json.JSONDecodeError:
                                        pass
                                except json.JSONDecodeError:
                                    tool_calls_builder[idx]["function"]["arguments"] += f_args

                        # 提取 Gemini 思考模型的 thought_signature（回传时需原样带回）
                        sig = _extract_thought_signature(tc_item)
                        if sig:
                            tool_calls_builder[idx]["thought_signature"] = sig

                # 真实流式实时 yield 给前端呈现（25ms 节流：格式化/全量拷贝成本高，
                # 被节流吞掉的中间态由轮次结束前的强制 flush 兜底补发）
                if r_chunk or c_chunk:
                    now = time.time()
                    if now - last_stream_emit >= _STREAM_EMIT_INTERVAL:
                        last_stream_emit = now
                        formatted_display = _format_display_message(accumulated_reasoning, accumulated_content)
                        if is_streaming and chat_display and chat_display[-1].get("role") == "assistant":
                            chat_display[-1] = {"role": "assistant", "content": formatted_display}
                        else:
                            chat_display.append({"role": "assistant", "content": formatted_display})
                            is_streaming = True
                        yield (
                            list(chat_display),
                            "",
                            updated_files,
                            new_undo_stack,
                            full_history,
                            download_path,
                        None,
                    )

            msg_tool_calls_list = [
                v for k, v in sorted(tool_calls_builder.items(), key=lambda x: x[0])
            ]

            # 节流吞帧兜底：轮次结束前强制补发完整流式帧（含思考块），
            # 保证展示内容与 accumulated_content/reasoning 始终一致
            if is_streaming or accumulated_reasoning or accumulated_content:
                formatted_display = _format_display_message(accumulated_reasoning, accumulated_content)
                if is_streaming and chat_display and chat_display[-1].get("role") == "assistant":
                    chat_display[-1] = {"role": "assistant", "content": formatted_display}
                else:
                    chat_display.append({"role": "assistant", "content": formatted_display})
                    is_streaming = True
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    None,
                )

            assistant_message: dict = {
                "role": "assistant",
                "content": accumulated_content,
            }
            # 推理过程随消息持久化（重开对话后仍能展示思考过程）；
            # 发送给 API 前由 _sanitize_messages 剥除，避免未知字段被拒
            if accumulated_reasoning:
                assistant_message["reasoning_content"] = accumulated_reasoning
            if msg_tool_calls_list:
                tool_calls_out = []
                for i, tc in enumerate(msg_tool_calls_list):
                    tc_out = {
                        "id": tc.get("id") or f"call_{i}",
                        "type": tc.get("type", "function"),
                        "function": {
                            "name": tc.get("function", {}).get("name", ""),
                            "arguments": tc.get("function", {}).get("arguments", ""),
                        },
                    }
                    if is_gemini:
                        # thought_signature 仅 Gemini 思考模型需要，其他服务商不认扩展字段
                        tc_out["extra_content"] = {
                            "google": {
                                "thought_signature": tc.get("thought_signature")
                                or "skip_thought_signature_validator"
                            }
                        }
                    tool_calls_out.append(tc_out)
                assistant_message["tool_calls"] = tool_calls_out

            messages.append(assistant_message)

            # ── 无工具调用 → 最终回复完成 ──
            if not msg_tool_calls_list:
                new_full_history = _finalize_response(
                    messages, full_history, message, _round_start_idx, updated_files,
                )
                formatted_final = _format_display_message(accumulated_reasoning, accumulated_content)
                if is_streaming and chat_display and chat_display[-1].get("role") == "assistant":
                    chat_display[-1] = {"role": "assistant", "content": formatted_final}
                else:
                    chat_display.append({"role": "assistant", "content": formatted_final})

                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    new_full_history,
                    download_path,
                    None,
                )
                return

            # ── 工具调用前说明文字 ──
            if accumulated_content and accumulated_content.strip():
                if not (is_streaming and chat_display and chat_display[-1].get("role") == "assistant"):
                    chat_display.append({"role": "assistant", "content": accumulated_content})
                yield (
                    list(chat_display),
                    "",
                    updated_files,
                    new_undo_stack,
                    full_history,
                    download_path,
                    None,
                )

            # ── 执行工具调用 ──
            for i, tc in enumerate(msg_tool_calls_list):
                tc_id = tc.get("id") or f"call_{i}"
                tc_name = tc.get("function", {}).get("name", "")
                tc_args_raw = tc.get("function", {}).get("arguments", "")

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
                    None,
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

                if tc_name == "create_midi":
                    filename = tc_args.get("filename", "")
                    try:
                        filepath = _chat_ui._resolve_created_midi_path(filename)
                    except ValueError as exc:
                        logger.warning("create_midi 下载路径无效: %s", exc)
                        filepath = None
                    if filepath is not None and filepath.is_file():
                        download_path = str(filepath)

                messages.append(
                    _chat_ui._make_tool_result_message(tc_id, result_text, name=tc_name),
                )

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
                    None,
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
        # 不直接 str(e) 暴露给 UI，避免 API 异常可能包含敏感请求信息
        status_code = getattr(e, "status_code", None)
        err_str = str(e).lower()
        if status_code == 429 or "429" in err_str or "rate" in err_str or "resource_exhausted" in err_str:
            error_msg = (
                "⚠ 调用失败: 触发 API 请求限流 (HTTP 429 / Rate Limit / Quota Exceeded)。\n\n"
                "当前 API 服务商或 API Key 的请求频率/免费额度已达上限。建议：\n"
                "1. **稍等 15~60 秒** 后重新发送消息（等待速率恢复）；\n"
                "2. 在主界面设置页切换额度充足的 API Key 或服务商；\n"
                "3. 切换为其他消耗更低的模型（如 Gemini 2.5 Flash / DeepSeek 聊天模型）。"
            )
        else:
            error_msg = (
                f"⚠ 调用失败: {type(e).__name__}"
                + (f" (HTTP {status_code})" if status_code else "")
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
            None,
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
            None,
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
