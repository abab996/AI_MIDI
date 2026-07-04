"""Tests for webui.py settings and validation logic."""
from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

import webui


class TestSaveSettings:
    def test_save_settings_persists_to_file(self, mock_config, settings_file):
        with patch.object(webui, "config") as mock_cfg:
            mock_cfg.save_settings = MagicMock()
            result = webui._save_settings(
                api_key="sk-test",
                base_url="https://api.deepseek.com",
                model="deepseek-v4-pro",
                max_tokens=1000,
                max_completion_tokens=500,
                reasoning_effort="max",
                thinking_enabled=True,
            )
            mock_cfg.save_settings.assert_called_once()
            saved = mock_cfg.save_settings.call_args[0][0]
            assert saved["api_key"] == "sk-test"
            assert saved["model"] == "deepseek-v4-pro"

    def test_save_settings_returns_success_message(self, mock_config):
        with patch.object(webui, "config") as mock_cfg:
            mock_cfg.save_settings = MagicMock()
            result = webui._save_settings(
                api_key="sk-test",
                base_url="https://api.deepseek.com",
                model="deepseek-v4-pro",
                max_tokens=None,
                max_completion_tokens=None,
                reasoning_effort="max",
                thinking_enabled=True,
            )
            assert "✓" in result


class TestBaseUrlValidation:
    def test_rejects_http(self):
        with pytest.raises(ValueError, match="https"):
            webui._validate_base_url("http://api.deepseek.com")

    def test_rejects_non_standard_port(self):
        with pytest.raises(ValueError, match="443"):
            webui._validate_base_url("https://api.deepseek.com:8443")

    def test_rejects_path(self):
        with pytest.raises(ValueError, match="路径"):
            webui._validate_base_url("https://api.deepseek.com/v1")

    def test_accepts_valid_https(self):
        result = webui._validate_base_url("https://api.deepseek.com")
        assert result == "https://api.deepseek.com"


class TestInputValidation:
    def test_invalid_bpm_does_not_crash(self):
        with patch.object(webui, "_validate_base_url", return_value="https://api.deepseek.com"), \
             patch.object(webui, "ai_api") as mock_ai:
            mock_ai.add_chord.return_value = "result"
            # Should not raise on invalid BPM
            webui._run_task(
                func=webui.FUNC_ADD_CHORD,
                note_table=[],
                bpm="not_a_number",
                time_signature="4/4",
                lyrics="",
                original_language="",
                target_language="",
                note_output=False,
                requirements="test",
                api_key="sk-test",
                base_url="https://api.deepseek.com",
                model="deepseek-v4-pro",
                max_tokens=None,
                max_completion_tokens=None,
                reasoning_effort="max",
                thinking_enabled=True,
            )
