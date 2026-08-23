package midi

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aimidi/internal/config"
)

type smfEvent struct {
	AbsTick  int64
	IsNoteOn bool
	Data     []byte
}

// TxtToMidi 将 note_table 文本或文件内容转换为标准 MIDI 文件并保存
func TxtToMidi(sourceText string, outputPath string, bpm int) ([]byte, error) {
	if outputPath == "" {
		outputPath = config.OutputMidi
	}

	notesInfo, err := ParseNoteTableLines(sourceText)
	if err != nil {
		return nil, err
	}

	midiBytes, err := BuildSMF(notesInfo, bpm)
	if err != nil {
		return nil, err
	}

	if outputPath != "" {
		_ = os.MkdirAll(filepath.Dir(outputPath), 0755)
		if err := os.WriteFile(outputPath, midiBytes, 0644); err != nil {
			return nil, fmt.Errorf("保存 MIDI 文件失败: %w", err)
		}
	}

	return midiBytes, nil
}

// OutNote 把 AI 返回的 note_table 文本写入 MIDI 文件
func OutNote(noteTable string, bpm int, outputPath string) error {
	_, err := TxtToMidi(noteTable, outputPath, bpm)
	return err
}

// BuildSMF 从音符事件列表构建标准 MIDI 文件字节流
func BuildSMF(notes []NoteEvent, bpm int) ([]byte, error) {
	if len(notes) == 0 {
		return nil, ErrEmptyNoteTable
	}

	clampedBpm := ClampBPM(bpm)
	tpb := config.TicksPerBeat

	// 60,000,000 / BPM = 微秒/拍
	tempo := uint32(60000000 / clampedBpm)

	var events []smfEvent

	for _, n := range notes {
		midiNum, err := NoteNameToMidiNumber(n.Note)
		if err != nil {
			continue
		}

		startTick := int64(math.Max(0, math.Round(n.Start*float64(tpb))))
		endTick := int64(math.Max(float64(startTick+1), math.Round(n.End*float64(tpb))))

		vel := ClampVelocity(n.Velocity)

		// Note On 事件
		events = append(events, smfEvent{
			AbsTick:  startTick,
			IsNoteOn: true,
			Data:     []byte{0x90, byte(midiNum), byte(vel)},
		})

		// Note Off 事件
		events = append(events, smfEvent{
			AbsTick:  endTick,
			IsNoteOn: false,
			Data:     []byte{0x80, byte(midiNum), 0x00},
		})
	}

	// 排序：绝对时间升序；同一时间 NoteOff 先于 NoteOn
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].AbsTick == events[j].AbsTick {
			// NoteOff (IsNoteOn == false) 优先
			return !events[i].IsNoteOn && events[j].IsNoteOn
		}
		return events[i].AbsTick < events[j].AbsTick
	})

	var trackBuf bytes.Buffer

	// 1. 写入 Set Tempo Meta 事件 (Delta=0, FF 51 03 tt tt tt)
	trackBuf.WriteByte(0x00) // Delta Time 0
	trackBuf.Write([]byte{0xFF, 0x51, 0x03})
	trackBuf.Write([]byte{
		byte((tempo >> 16) & 0xFF),
		byte((tempo >> 8) & 0xFF),
		byte(tempo & 0xFF),
	})

	// 2. 写入音符事件
	var lastTick int64
	for _, ev := range events {
		delta := ev.AbsTick - lastTick
		if delta < 0 {
			delta = 0
		}
		writeVarLength(&trackBuf, uint32(delta))
		trackBuf.Write(ev.Data)
		lastTick = ev.AbsTick
	}

	// 3. 写入 End of Track Meta 事件 (Delta=0, FF 2F 00)
	trackBuf.WriteByte(0x00)
	trackBuf.Write([]byte{0xFF, 0x2F, 0x00})

	// 4. 构建完整 SMF 文件
	var smfBuf bytes.Buffer

	// MThd Header: Format 0, 1 Track, Division tpb
	smfBuf.WriteString("MThd")
	_ = binary.Write(&smfBuf, binary.BigEndian, uint32(6))
	_ = binary.Write(&smfBuf, binary.BigEndian, uint16(0))
	_ = binary.Write(&smfBuf, binary.BigEndian, uint16(1))
	_ = binary.Write(&smfBuf, binary.BigEndian, uint16(tpb))

	// MTrk Track Chunk
	smfBuf.WriteString("MTrk")
	_ = binary.Write(&smfBuf, binary.BigEndian, uint32(trackBuf.Len()))
	smfBuf.Write(trackBuf.Bytes())

	return smfBuf.Bytes(), nil
}

func writeVarLength(buf *bytes.Buffer, value uint32) {
	buffer := value & 0x7F
	for {
		value >>= 7
		if value == 0 {
			break
		}
		buffer <<= 8
		buffer |= ((value & 0x7F) | 0x80)
	}

	for {
		buf.WriteByte(byte(buffer & 0xFF))
		if (buffer & 0x80) != 0 {
			buffer >>= 8
		} else {
			break
		}
	}
}

// NormalizeNoteData 兼容 AI 返回的 JSON 数组格式或 note_table 文本
func NormalizeNoteData(raw string) string {
	clean := strings.TrimSpace(raw)
	return clean
}
