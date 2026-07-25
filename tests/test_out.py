from __future__ import annotations

from pathlib import Path

import mido
import pytest

import get
import out


NOTE = '[note: "C-1", velocity: "80", start: "0", end: "0"]'


def test_c_minus_one_and_zero_duration_write_one_tick_note(tmp_path):
    output = tmp_path / "roundtrip.mid"

    out.txt_to_midi(NOTE, output)

    messages = [msg for msg in mido.MidiFile(output) if msg.type in {"note_on", "note_off"}]
    assert [msg.note for msg in messages] == [0, 0]
    assert messages[1].time > 0


def test_existing_source_path_with_bracket_is_read_as_file(tmp_path):
    source = tmp_path / "notes[demo].txt"
    source.write_text(NOTE, encoding="utf-8")
    output = tmp_path / "from-file.mid"

    out.txt_to_midi(source, output)

    assert output.is_file()


def test_missing_pathlike_source_raises(tmp_path):
    with pytest.raises(FileNotFoundError):
        out.txt_to_midi(Path(tmp_path / "missing[notes].txt"), tmp_path / "unused.mid")


def test_get_out_get_roundtrip_preserves_one_tick_c_minus_one(tmp_path):
    source = mido.MidiFile(ticks_per_beat=480)
    track = mido.MidiTrack()
    source.tracks.append(track)
    track.append(mido.Message("note_on", note=0, velocity=80, time=0))
    track.append(mido.Message("note_off", note=0, velocity=0, time=1))
    source_path = tmp_path / "source.mid"
    source.save(source_path)

    first_note_table = get.get_note(str(source_path), save_to_file=False)
    roundtrip_path = tmp_path / "roundtrip.mid"
    out.txt_to_midi("\n".join(first_note_table), roundtrip_path)
    second_note_table = get.get_note(str(roundtrip_path), save_to_file=False)

    assert len(second_note_table) == 1
    assert 'note: "C-1"' in second_note_table[0]
