"""Tests for mcp_server.py path traversal prevention."""
from __future__ import annotations

import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import mcp_server


class TestPathTraversal:
    def test_parse_midi_rejects_dotdot(self, tmp_project):
        mcp_server.OUTPUT_DIR = tmp_project / "output"
        result = mcp_server.parse_midi("../../etc/passwd")
        assert "错误" in result or "not allowed" in result.lower()

    def test_create_midi_rejects_dotdot(self, tmp_project):
        mcp_server.OUTPUT_DIR = tmp_project / "output"
        result = mcp_server.create_midi(
            "../../evil.mid",
            notes='[note: "C4", velocity: "80", start: "1", end: "2"]',
        )
        assert "错误" in result or "not allowed" in result.lower()

    def test_delete_midi_rejects_dotdot(self, tmp_project):
        mcp_server.OUTPUT_DIR = tmp_project / "output"
        result = mcp_server.delete_midi("../../evil.mid")
        assert "错误" in result or "not allowed" in result.lower()

    def test_read_library_file_rejects_dotdot(self, tmp_project):
        mcp_server.config.PROJECT_ROOT = tmp_project
        result = mcp_server.read_library_file("../../etc/passwd")
        assert "错误" in result or "not allowed" in result.lower()
