"""Tests for ai_api.py."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).parent.parent))

import ai_api


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
