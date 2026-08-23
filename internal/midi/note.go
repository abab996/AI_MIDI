package midi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"aimidi/internal/config"
)

var noteNames = []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

var notesMap = map[string]int{
	"C": 0, "C#": 1, "Db": 1,
	"D": 2, "D#": 3, "Eb": 3,
	"E": 4, "Fb": 4,
	"F": 5, "E#": 5,
	"F#": 6, "Gb": 6,
	"G": 7, "G#": 8, "Ab": 8,
	"A": 9, "A#": 10, "Bb": 10,
	"B": 11, "Cb": 11,
}

var noteNameRe = regexp.MustCompile(`(?i)^([A-G][#b]*)`)

// MidiNumberToNoteName 将 MIDI 音高编号(0-127)转换为音符名称，如 C4。无效输入返回 "Invalid"
func MidiNumberToNoteName(midiNote int) string {
	if midiNote < config.NoteNumberMin || midiNote > config.NoteNumberMax {
		return "Invalid"
	}
	octave := (midiNote / config.NotesPerOctave) - 1
	note := noteNames[midiNote%config.NotesPerOctave]
	return fmt.Sprintf("%s%d", note, octave)
}

// NoteNameToMidiNumber 将音符名称(如 C4、Eb3、C##4)转换为 MIDI 编号(0-127)
func NoteNameToMidiNumber(noteName string) (int, error) {
	cleanName := strings.TrimSpace(noteName)
	match := noteNameRe.FindStringSubmatch(cleanName)
	if len(match) < 2 {
		return 0, fmt.Errorf("无法识别的音符名称: %s", noteName)
	}

	rawPitch := match[1]
	// 首字母大写，升降号小写
	pitch := strings.ToUpper(string(rawPitch[0])) + strings.ToLower(rawPitch[1:])
	octaveStr := cleanName[len(rawPitch):]

	octave, err := strconv.Atoi(octaveStr)
	if err != nil {
		return 0, fmt.Errorf("无法识别的音符名称: %s (八度部分 '%s' 无效)", noteName, octaveStr)
	}

	// 计算升降号偏移量（支持多升降号如 C##、Bbb）
	lowerPitch := strings.ToLower(rawPitch[1:])
	accidentalOffset := strings.Count(lowerPitch, "#") - strings.Count(lowerPitch, "b")

	basePitch := string(pitch[0])
	baseMidi, ok := notesMap[basePitch]
	if !ok {
		return 0, fmt.Errorf("无法识别的基础音名: %s", basePitch)
	}

	midiNum := (octave+1)*12 + baseMidi + accidentalOffset
	return ClampMidiNumber(midiNum), nil
}

// ClampMidiNumber 限制在 0-127
func ClampMidiNumber(n int) int {
	if n < config.NoteNumberMin {
		return config.NoteNumberMin
	}
	if n > config.NoteNumberMax {
		return config.NoteNumberMax
	}
	return n
}

// ClampVelocity 限制力度在 0-127
func ClampVelocity(v int) int {
	if v < config.MinVelocity {
		return config.MinVelocity
	}
	if v > config.MaxVelocity {
		return config.MaxVelocity
	}
	return v
}

// ClampBPM 限制 BPM 在 1-600
func ClampBPM(b int) int {
	if b < config.BPMMin {
		return config.BPMMin
	}
	if b > config.BPMMax {
		return config.BPMMax
	}
	return b
}
