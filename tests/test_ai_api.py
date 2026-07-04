"""Tests for ai_api.py."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).parent.parent))

import ai_api
import openai


class TestApiTimeout:
    def test_chat_passes_timeout_to_client(self):
        mock_client = MagicMock()
        mock_response = MagicMock()
        mock_response.choices = [MagicMock(message=MagicMock(content="test"))]
        mock_client.chat.completions.create.return_value = mock_response
        
        with patch.object(ai_api, "get_client", return_value=mock_client):
            ai_api._chat("hello", timeout=30)
        
        _, kwargs = mock_client.chat.completions.create.call_args
        assert kwargs.get("timeout") == 30

    def test_chat_stream_passes_timeout_to_client(self):
        mock_client = MagicMock()
        mock_response = MagicMock()
        mock_response.__iter__ = lambda self: iter([])
        mock_client.chat.completions.create.return_value = mock_response
        
        with patch.object(ai_api, "get_client", return_value=mock_client):
            list(ai_api._chat_stream([{"role": "user", "content": "hi"}], timeout=30))
        
        _, kwargs = mock_client.chat.completions.create.call_args
        assert kwargs.get("timeout") == 30


class TestTypeErrorHandling:
    def test_chat_returns_empty_string_on_type_error(self):
        mock_client = MagicMock()
        mock_client.chat.completions.create.side_effect = TypeError("unexpected keyword argument")
        
        with patch.object(ai_api, "get_client", return_value=mock_client):
            result = ai_api._chat("hello", timeout=30)
            assert result == ""

    def test_chat_stream_returns_empty_iterator_on_type_error(self):
        mock_client = MagicMock()
        mock_client.chat.completions.create.side_effect = TypeError("unexpected keyword argument")
        
        with patch.object(ai_api, "get_client", return_value=mock_client):
            result = list(ai_api._chat_stream([{"role": "user", "content": "hi"}], timeout=30))
            assert result == []


class TestChatStream:
    def test_stream_returns_empty_iterator_on_api_error(self):
        mock_client = MagicMock()
        mock_client.chat.completions.create.side_effect = openai.APIError(
            message="test", request=None, body=None
        )
        
        with patch.object(ai_api, "get_client", return_value=mock_client):
            result = list(ai_api._chat_stream([{"role": "user", "content": "hi"}]))
            assert result == []
