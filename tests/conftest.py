"""Shared pytest fixtures for AI_MIDI test suite."""
from __future__ import annotations

import json
import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

# Add project root to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent))


@pytest.fixture
def tmp_project(tmp_path: Path) -> Path:
    """Create a temporary project root with required subdirectories."""
    (tmp_path / "input").mkdir()
    (tmp_path / "output").mkdir()
    (tmp_path / "doing").mkdir()
    (tmp_path / "projects").mkdir()
    (tmp_path / "Library").mkdir()
    return tmp_path


@pytest.fixture
def mock_config(tmp_project: Path):
    """Patch config module to use temporary directories."""
    # Import here to avoid requiring all deps at conftest import time
    import config

    with patch.object(config, "PROJECT_ROOT", tmp_project), \
         patch.object(config, "INPUT_MIDI", tmp_project / "input" / "in.mid"), \
         patch.object(config, "OUTPUT_DIR", tmp_project / "output"), \
         patch.object(config, "OUTPUT_MIDI", tmp_project / "output.mid"), \
         patch.object(config, "DOING_DIR", tmp_project / "doing"), \
         patch.object(config, "DOING_OUTPUT_TXT", tmp_project / "doing" / "midi_output.txt"), \
         patch.object(config, "PROJECTS_DIR", tmp_project / "projects"), \
         patch.object(config, "SETTINGS_FILE", tmp_project / "settings.json"), \
         patch.object(config, "DEFAULT_BPM", 120), \
         patch.object(config, "DEFAULT_TIME_SIGNATURE", "4/4"), \
         patch.object(config, "BASE_URL", "https://api.deepseek.com"), \
         patch.object(config, "MODEL", "deepseek-v4-pro"), \
         patch.object(config, "TICKS_PER_BEAT", 480):
        yield tmp_project


@pytest.fixture
def settings_file(tmp_project: Path) -> Path:
    """Create an empty settings.json."""
    sf = tmp_project / "settings.json"
    sf.write_text("{}", encoding="utf-8")
    return sf
