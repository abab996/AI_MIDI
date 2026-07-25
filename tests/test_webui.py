from __future__ import annotations

import config
import webui


def test_default_midi_output_is_in_gradio_allowed_directory():
    output = config.OUTPUT_MIDI.resolve()
    allowed = [__import__("pathlib").Path(path).resolve() for path in webui._build_allowed_paths()]

    assert any(output.is_relative_to(directory) for directory in allowed)
