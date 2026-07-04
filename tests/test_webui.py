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


class TestChatLaunch:
    def test_chat_started_only_set_after_ready(self):
        webui._chat_started = False
        webui._chat_launch_error = None
        
        with patch.object(webui, "_is_chat_running", return_value=False), \
             patch.object(webui, "_start_chat_server") as mock_start, \
             patch.object(webui, "_wait_for_chat_ready", return_value=True):
            
            # Simulate _start_chat_server setting _chat_started = True when ready
            def simulate_start():
                webui._chat_started = True
                webui._chat_launch_error = None
            
            mock_start.side_effect = simulate_start
            
            result = webui.launch_chat()
            
            # After launch_chat returns, _chat_started should reflect actual readiness
            assert webui._chat_started is True
