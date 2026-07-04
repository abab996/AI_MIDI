"""Tests for chat_ui.py subprocess and MCP management."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).parent.parent))

import chat_ui


class TestMcpProcess:
    def test_stderr_is_not_pipe(self):
        with patch("subprocess.Popen") as mock_popen:
            mock_proc = MagicMock()
            mock_proc.poll.return_value = None
            mock_popen.return_value = mock_proc
            chat_ui._ensure_mcp_process()
            _, kwargs = mock_popen.call_args
            assert kwargs.get("stderr") != subprocess.PIPE
