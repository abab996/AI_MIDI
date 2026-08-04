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
