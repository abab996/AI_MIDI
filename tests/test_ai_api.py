from __future__ import annotations

from unittest.mock import MagicMock, patch

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
