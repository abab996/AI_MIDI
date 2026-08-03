from __future__ import annotations

from pathlib import Path

import config
import server


def test_default_midi_output_is_in_allowed_download_directory():
    output = config.OUTPUT_MIDI.resolve()
    allowed = [Path(p).resolve() for p in server._build_allowed_paths()]

    assert any(output.is_relative_to(directory) for directory in allowed)


def test_chat_project_dir_is_in_allowed_download_directory():
    projects = config.PROJECTS_DIR.resolve()
    allowed = [Path(p).resolve() for p in server._build_allowed_paths()]

    assert any(projects.is_relative_to(directory) for directory in allowed)
