from __future__ import annotations

import re

from mido import Message, MidiFile, MidiTrack

import get


def test_one_tick_c_minus_one_note_keeps_nonzero_duration(tmp_path):
    mid = MidiFile(ticks_per_beat=480)
    track = MidiTrack()
    mid.tracks.append(track)
    track.append(Message("note_on", note=0, velocity=80, time=0))
    track.append(Message("note_off", note=0, velocity=0, time=1))
    source = tmp_path / "short.mid"
    mid.save(source)

    result = get.parse_midi_to_custom_format(str(source))

    assert len(result) == 1
    assert 'note: "C-1"' in result[0]
    fields = re.findall(r'(?:start|end): "([\d.]+)"', result[0])
    assert len(fields) == 2
    assert float(fields[1]) > float(fields[0])
