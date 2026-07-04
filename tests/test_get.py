"""Tests for get.py MIDI parsing."""
from __future__ import annotations

import sys
from pathlib import Path

import mido
from mido import MidiFile, MidiTrack, Message

sys.path.insert(0, str(Path(__file__).parent.parent))

import get


def create_midi(tmp_path: Path, events: list) -> Path:
    """Helper to create a test MIDI file from event list."""
    mid = MidiFile()
    track = MidiTrack()
    mid.tracks.append(track)
    for msg in events:
        track.append(msg)
    path = tmp_path / "test.mid"
    mid.save(str(path))
    return path


class TestNoteHandling:
    def test_repeated_note_on_creates_two_notes(self, tmp_project):
        events = [
            Message("note_on", note=60, velocity=80, time=0),
            Message("note_on", note=60, velocity=80, time=10),
            Message("note_off", note=60, velocity=0, time=20),
        ]
        path = create_midi(tmp_project, events)
        result = get.parse_midi_to_custom_format(str(path))
        assert len(result) == 2

    def test_note_without_note_off_is_included(self, tmp_project):
        events = [
            Message("note_on", note=60, velocity=80, time=0),
            Message("note_on", note=62, velocity=80, time=10),
        ]
        path = create_midi(tmp_project, events)
        result = get.parse_midi_to_custom_format(str(path))
        assert len(result) == 2
