from __future__ import annotations

from unittest.mock import MagicMock, patch

import httpx
import openai

import ai_api


def test_is_upstream_transient():
    def err(status):
        e = openai.APIStatusError("boom", response=MagicMock(), body=None)
        e.status_code = status
        return e

    for status in (429, 500, 502, 503, 504):
        assert ai_api.is_upstream_transient(err(status)), status
    assert not ai_api.is_upstream_transient(err(400))
    assert not ai_api.is_upstream_transient(err(401))

    assert ai_api.is_upstream_transient(openai.APITimeoutError("timeout"))
    assert ai_api.is_upstream_transient(httpx.ConnectError("conn"))
    assert not ai_api.is_upstream_transient(openai.BadRequestError(
        "Bad Request: unknown field 'thinking'", response=MagicMock(), body=None,
    ))


def test_chat_retries_upstream_503_then_succeeds():
    client = MagicMock()
    err = openai.APIStatusError("service unavailable", response=MagicMock(), body=None)
    err.status_code = 503
    client.chat.completions.create.side_effect = [
        err,
        MagicMock(choices=[MagicMock(message=MagicMock(content="ok"))]),
    ]

    with patch.object(ai_api, "get_client", return_value=client), patch("ai_api.time.sleep"):
        assert ai_api._chat("hello") == "ok"

    assert client.chat.completions.create.call_count == 2


def test_chat_returns_empty_after_three_upstream_failures():
    client = MagicMock()
    err = openai.APIStatusError("server busy", response=MagicMock(), body=None)
    err.status_code = 503
    client.chat.completions.create.side_effect = [err] * 4

    with patch.object(ai_api, "get_client", return_value=client), patch("ai_api.time.sleep"):
        assert ai_api._chat("hello") == ""

    assert client.chat.completions.create.call_count == 4


def test_chat_passes_timeout_to_client():
    client = MagicMock()
    client.chat.completions.create.return_value.choices = [
        MagicMock(message=MagicMock(content="ok")),
    ]

    with patch.object(ai_api, "get_client", return_value=client):
        assert ai_api._chat("hello", timeout=30) == "ok"

    assert client.chat.completions.create.call_args.kwargs["timeout"] == 30


def test_chat_returns_empty_string_for_unsupported_api_arguments():
    client = MagicMock()
    client.chat.completions.create.side_effect = TypeError("unexpected argument")

    with patch.object(ai_api, "get_client", return_value=client):
        assert ai_api._chat("hello") == ""


def test_chat_does_not_send_thinking_for_non_gemini_provider():
    client = MagicMock()
    client.chat.completions.create.return_value.choices = [
        MagicMock(message=MagicMock(content="ok")),
    ]

    with patch.object(ai_api, "get_client", return_value=client):
        assert (
            ai_api._chat(
                "hello",
                base_url="https://api.deepseek.com",
                thinking_enabled=True,
            )
            == "ok"
        )

    kwargs = client.chat.completions.create.call_args.kwargs
    assert "extra_body" not in kwargs


def test_chat_sends_thinking_only_for_gemini_provider():
    client = MagicMock()
    client.chat.completions.create.return_value.choices = [
        MagicMock(message=MagicMock(content="ok")),
    ]

    with patch.object(ai_api, "get_client", return_value=client):
        assert (
            ai_api._chat(
                "hello",
                base_url="https://generativelanguage.googleapis.com",
                thinking_enabled=True,
            )
            == "ok"
        )

    kwargs = client.chat.completions.create.call_args.kwargs
    assert kwargs["extra_body"] == {"thinking": {"type": "enabled"}}


def test_chat_strips_thinking_on_plain_400_and_retries():
    client = MagicMock()
    err = openai.BadRequestError(
        "Bad Request: unknown field 'thinking'",
        response=MagicMock(),
        body=None,
    )
    client.chat.completions.create.side_effect = [
        err,
        MagicMock(choices=[MagicMock(message=MagicMock(content="ok"))]),
    ]

    with patch.object(ai_api, "get_client", return_value=client):
        assert (
            ai_api._chat(
                "hello",
                base_url="https://generativelanguage.googleapis.com",
                thinking_enabled=True,
            )
            == "ok"
        )

    first_kwargs = client.chat.completions.create.call_args_list[0].kwargs
    assert "extra_body" in first_kwargs
    second_kwargs = client.chat.completions.create.call_args_list[1].kwargs
    assert "extra_body" not in second_kwargs


def test_chat_strips_max_tokens_on_400_and_retries():
    """服务商不认 max_tokens/max_completion_tokens 时 400 → 剥离后重试。

    回归：此前 _strippable 不含 max_* 参数，这类 400 不会自动剥离，
    只能把错误直接抛给用户。
    剥离按列表顺序逐个推进（每次 400 只剥一个）：
    extra_body → max_tokens → max_completion_tokens。
    """
    client = MagicMock()
    err = openai.BadRequestError(
        "Bad Request: unknown field 'max_tokens'",
        response=MagicMock(),
        body=None,
    )
    client.chat.completions.create.side_effect = [
        err,          # 400：剥 extra_body
        err,          # 400：剥 max_tokens
        err,          # 400：剥 max_completion_tokens
        MagicMock(choices=[MagicMock(message=MagicMock(content="ok"))]),
    ]

    with patch.object(ai_api, "get_client", return_value=client):
        assert (
            ai_api._chat(
                "hello",
                base_url="https://generativelanguage.googleapis.com",
                max_tokens=2048,
                max_completion_tokens=4096,
            )
            == "ok"
        )

    calls = client.chat.completions.create.call_args_list
    assert calls[0].kwargs["max_tokens"] == 2048
    assert calls[0].kwargs["max_completion_tokens"] == 4096
    assert "extra_body" in calls[0].kwargs
    assert "extra_body" not in calls[1].kwargs
    assert calls[1].kwargs["max_tokens"] == 2048        # 本轮剥的是 extra_body
    assert "max_tokens" not in calls[2].kwargs          # 本轮剥的是 max_tokens
    assert calls[2].kwargs["max_completion_tokens"] == 4096
    assert "max_completion_tokens" not in calls[3].kwargs
