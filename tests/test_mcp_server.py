from __future__ import annotations

import mcp_server


def test_list_midi_files_includes_mid_and_midi_case_insensitively(tmp_path):
    (tmp_path / "a.mid").write_bytes(b"x")
    (tmp_path / "b.midi").write_bytes(b"x")
    (tmp_path / "c.MIDI").write_bytes(b"x")
    (tmp_path / "ignore.txt").write_text("x", encoding="utf-8")
    previous = mcp_server.OUTPUT_DIR
    mcp_server.OUTPUT_DIR = tmp_path
    try:
        result = mcp_server.list_midi_files()
    finally:
        mcp_server.OUTPUT_DIR = previous

    assert "a.mid" in result
    assert "b.midi" in result
    assert "c.MIDI" in result
    assert "ignore.txt" not in result


def test_create_midi_rejects_path_traversal(tmp_path):
    previous = mcp_server.OUTPUT_DIR
    mcp_server.OUTPUT_DIR = tmp_path
    try:
        result = mcp_server.create_midi(
            "../escape.mid",
            notes='[note: "C4", velocity: "80", start: "0", end: "1"]',
        )
    finally:
        mcp_server.OUTPUT_DIR = previous

    assert "错误" in result
