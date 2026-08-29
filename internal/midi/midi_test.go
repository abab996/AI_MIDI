package midi

import (
	"testing"
)

func TestNoteConversions(t *testing.T) {
	cases := []struct {
		name     string
		noteName string
		midiNum  int
		wantErr  bool
	}{
		{"C4", "C4", 60, false},
		{"A4", "A4", 69, false},
		{"C#4", "C#4", 61, false},
		{"Db4", "Db4", 61, false},
		{"Eb3", "Eb3", 51, false},
		{"C##4", "C##4", 62, false},
		{"Bbb3", "Bbb3", 57, false},
		{"F##5", "F##5", 79, false},
		{"B#3", "B#3", 60, false},
		{"Fb4", "Fb4", 64, false},
		{"Invalid", "XYZ", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			num, err := NoteNameToMidiNumber(tc.noteName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NoteNameToMidiNumber(%s) err = %v, wantErr = %v", tc.noteName, err, tc.wantErr)
			}
			if !tc.wantErr && num != tc.midiNum {
				t.Fatalf("NoteNameToMidiNumber(%s) = %d, want %d", tc.noteName, num, tc.midiNum)
			}
		})
	}
}

func TestMidiNumberToNoteName(t *testing.T) {
	if n := MidiNumberToNoteName(60); n != "C4" {
		t.Errorf("MidiNumberToNoteName(60) = %s, want C4", n)
	}
	if n := MidiNumberToNoteName(69); n != "A4" {
		t.Errorf("MidiNumberToNoteName(69) = %s, want A4", n)
	}
	if n := MidiNumberToNoteName(-1); n != "Invalid" {
		t.Errorf("MidiNumberToNoteName(-1) = %s, want Invalid", n)
	}
	if n := MidiNumberToNoteName(128); n != "Invalid" {
		t.Errorf("MidiNumberToNoteName(128) = %s, want Invalid", n)
	}
}

func TestNoteTableExtraction(t *testing.T) {
	text := `
这里是 AI 的回答内容：
[note: "C4", velocity: "80", start: "1.0", end: "2.0"]
一些废话
[note: "E4", velocity: "90", start: "2.0", end: "3.5"]
[note: "G4", velocity: "85", start: "3.5", end: "4.0"]
`
	notes, err := ParseNoteTableLines(text)
	if err != nil {
		t.Fatalf("ParseNoteTableLines failed: %v", err)
	}
	if len(notes) != 3 {
		t.Fatalf("got %d notes, want 3", len(notes))
	}
	if notes[0].Note != "C4" || notes[0].Velocity != 80 || notes[0].Start != 1.0 || notes[0].End != 2.0 {
		t.Errorf("note 0 mismatch: %+v", notes[0])
	}
}

func TestSMFRoundtrip(t *testing.T) {
	text := `[note: "C4", velocity: "80", start: "1.0", end: "2.0"]
[note: "E4", velocity: "90", start: "2.0", end: "3.0"]
[note: "G4", velocity: "85", start: "3.0", end: "4.0"]`

	midiBytes, err := TxtToMidi(text, "", 120)
	if err != nil {
		t.Fatalf("TxtToMidi failed: %v", err)
	}

	noteStrings, err := ParseMidiToCustomFormat(midiBytes)
	if err != nil {
		t.Fatalf("ParseMidiToCustomFormat failed: %v", err)
	}

	if len(noteStrings) != 3 {
		t.Fatalf("roundtrip got %d notes, want 3", len(noteStrings))
	}
}

func TestJSONNoteTableParsing(t *testing.T) {
	// 测试标准多行 JSON 数组
	jsonText := `[
  {
    "note": "C4",
    "velocity": 85,
    "start": 0.0,
    "end": 1.0
  },
  {
    "note": "E4",
    "velocity": "90",
    "start": "1.0",
    "end": "2.0"
  }
]`
	notes, err := ParseNoteTableLines(jsonText)
	if err != nil {
		t.Fatalf("ParseNoteTableLines JSON failed: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("got %d notes, want 2", len(notes))
	}
	if notes[0].Note != "C4" || notes[0].Velocity != 85 || notes[0].Start != 0.0 || notes[0].End != 1.0 {
		t.Errorf("note 0 mismatch: %+v", notes[0])
	}
	if notes[1].Note != "E4" || notes[1].Velocity != 90 || notes[1].Start != 1.0 || notes[1].End != 2.0 {
		t.Errorf("note 1 mismatch: %+v", notes[1])
	}

	// 测试带有 ```json ``` 代码块包裹的内容
	fencedText := "```json\n" + jsonText + "\n```"
	fencedNotes, err := ParseNoteTableLines(fencedText)
	if err != nil {
		t.Fatalf("ParseNoteTableLines fenced JSON failed: %v", err)
	}
	if len(fencedNotes) != 2 {
		t.Fatalf("got %d fenced notes, want 2", len(fencedNotes))
	}
}
