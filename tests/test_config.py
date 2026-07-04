"""Tests for config.py logging setup."""
from __future__ import annotations

import importlib
import sys
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent.parent))

import config


class TestConfigImport:
    def test_import_does_not_crash_when_mkdir_fails(self):
        original_mkdir = Path.mkdir
        def failing_mkdir(self, *args, **kwargs):
            raise PermissionError("Access denied")
        
        Path.mkdir = failing_mkdir
        try:
            importlib.reload(config)
        finally:
            Path.mkdir = original_mkdir
            importlib.reload(config)
