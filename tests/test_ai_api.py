from __future__ import annotations

from unittest.mock import MagicMock, patch

import openai

import ai_api


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
