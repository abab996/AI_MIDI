"""Tests for out.py MIDI generation."""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

import out


class TestRegexParsing:
    def test_fields_in_any_order_are_parsed(self):
        lines = [
            '[note: "C4", velocity: "80", start: "1", end: "2"]',
            '[start: "2", note: "D4", end: "3", velocity: "60"]',
            'velocity: 70, end: 4, note: E4, start: 3',
        ]
        notes, count = out._parse_lines(lines)
        assert len(notes) == 3
        assert notes[0]["note"] == "C4"
        assert notes[1]["note"] == "D4"
        assert notes[2]["note"] == "E4"
