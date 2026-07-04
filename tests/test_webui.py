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
