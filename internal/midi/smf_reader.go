package midi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"

	"aimidi/internal/config"
)

// RawMidiEvent 内部用于多轨时间归并的原始 MIDI 事件
type RawMidiEvent struct {
	AbsTick  int64
	MsgType  string // "note_on", "note_off", "meta"
	Channel  int
	Note     int
	Velocity int
}

// ParseMidiToCustomFormat 解析 MIDI 字节流并返回 note_table 格式字符串列表
func ParseMidiToCustomFormat(data []byte) ([]string, error) {
	events, tpb, err := parseSMF(data)
	if err != nil {
		return nil, err
	}

	if tpb <= 0 {
		tpb = config.TicksPerBeat
	}

	// 稳定排序：按绝对 Tick 排序
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].AbsTick < events[j].AbsTick
	})

	type activeNoteInfo struct {
		startTick int64
		velocity  int
	}

	type channelNoteKey struct {
		channel int
		note    int
	}

	activeNotes := make(map[channelNoteKey]activeNoteInfo)
	var parsedNotes []NoteEvent
	var maxTick int64

	for _, ev := range events {
		if ev.AbsTick > maxTick {
			maxTick = ev.AbsTick
		}

		key := channelNoteKey{channel: ev.Channel, note: ev.Note}

		if ev.MsgType == "note_on" && ev.Velocity > 0 {
			if oldInfo, ok := activeNotes[key]; ok {
				delete(activeNotes, key)
				startBeat := math.Round((float64(oldInfo.startTick)/float64(tpb))*1e6) / 1e6
				endBeat := math.Round((float64(ev.AbsTick)/float64(tpb))*1e6) / 1e6
				if endBeat >= startBeat {
					parsedNotes = append(parsedNotes, NoteEvent{
						Note:     MidiNumberToNoteName(ev.Note),
						Velocity: oldInfo.velocity,
						Start:    startBeat,
						End:      endBeat,
					})
				}
			}
			activeNotes[key] = activeNoteInfo{
				startTick: ev.AbsTick,
				velocity:  ev.Velocity,
			}
		} else if ev.MsgType == "note_off" || (ev.MsgType == "note_on" && ev.Velocity == 0) {
			if info, ok := activeNotes[key]; ok {
				delete(activeNotes, key)
				startBeat := math.Round((float64(info.startTick)/float64(tpb))*1e6) / 1e6
				endBeat := math.Round((float64(ev.AbsTick)/float64(tpb))*1e6) / 1e6
				if endBeat >= startBeat {
					parsedNotes = append(parsedNotes, NoteEvent{
						Note:     MidiNumberToNoteName(ev.Note),
						Velocity: info.velocity,
						Start:    startBeat,
						End:      endBeat,
					})
				}
			}
		}
	}

	// 处理末尾悬挂的音符
	for key, info := range activeNotes {
		startBeat := math.Round((float64(info.startTick)/float64(tpb))*1e6) / 1e6
		endBeat := math.Round((float64(maxTick)/float64(tpb))*1e6) / 1e6
		if endBeat <= startBeat {
			endBeat = math.Round((startBeat+config.DanglingEpsilon)*1e6) / 1e6
		}
		if endBeat >= startBeat {
			parsedNotes = append(parsedNotes, NoteEvent{
				Note:     MidiNumberToNoteName(key.note),
				Velocity: info.velocity,
				Start:    startBeat,
				End:      endBeat,
			})
		}
	}

	// 按起始时间排序
	sort.SliceStable(parsedNotes, func(i, j int) bool {
		if parsedNotes[i].Start == parsedNotes[j].Start {
			return parsedNotes[i].End < parsedNotes[j].End
		}
		return parsedNotes[i].Start < parsedNotes[j].Start
	})

	var result []string
	for _, n := range parsedNotes {
		result = append(result, FormatNoteEvent(n))
	}

	return result, nil
}

// parseSMF 解析 Standard MIDI File 二进制流
func parseSMF(data []byte) ([]RawMidiEvent, int, error) {
	r := bytes.NewReader(data)

	// 1. 读取 MThd Chunk
	var headerTag [4]byte
	if _, err := io.ReadFull(r, headerTag[:]); err != nil {
		return nil, 0, fmt.Errorf("读取 MIDI 头失败: %w", err)
	}
	if string(headerTag[:]) != "MThd" {
		return nil, 0, errors.New("不是有效的 MIDI 文件（缺少 MThd 头）")
	}

	var headerLen uint32
	if err := binary.Read(r, binary.BigEndian, &headerLen); err != nil {
		return nil, 0, err
	}
	if headerLen < 6 {
		return nil, 0, errors.New("无效的 MThd 头长度")
	}

	var format, ntracks, division uint16
	if err := binary.Read(r, binary.BigEndian, &format); err != nil {
		return nil, 0, err
	}
	if err := binary.Read(r, binary.BigEndian, &ntracks); err != nil {
		return nil, 0, err
	}
	if err := binary.Read(r, binary.BigEndian, &division); err != nil {
		return nil, 0, err
	}

	// 跳过 headerLen > 6 的额外字节
	if headerLen > 6 {
		if _, err := io.CopyN(io.Discard, r, int64(headerLen-6)); err != nil {
			return nil, 0, err
		}
	}

	tpb := int(division)
	if division&0x8000 != 0 {
		// SMPTE 时间格式，默认按 480 处理
		tpb = config.TicksPerBeat
	}

	var allEvents []RawMidiEvent

	// 2. 读取各个 MTrk Chunk
	for t := 0; t < int(ntracks); t++ {
		var trackTag [4]byte
		if _, err := io.ReadFull(r, trackTag[:]); err != nil {
			break
		}
		if string(trackTag[:]) != "MTrk" {
			// 如果有未知 chunk，跳过
			var chunkLen uint32
			if err := binary.Read(r, binary.BigEndian, &chunkLen); err != nil {
				break
			}
			_, _ = io.CopyN(io.Discard, r, int64(chunkLen))
			t--
			continue
		}

		var trackLen uint32
		if err := binary.Read(r, binary.BigEndian, &trackLen); err != nil {
			return nil, 0, err
		}
		// 声明长度必须以文件实际剩余字节为上界：恶意/损坏文件可声明
		// 最大 4GB，先分配后读取会直接 OOM 杀死整个桌面应用
		if int64(trackLen) > int64(r.Len()) {
			return nil, 0, fmt.Errorf("MTrk 声明长度 %d 超出文件剩余 %d 字节", trackLen, r.Len())
		}

		trackData := make([]byte, trackLen)
		if _, err := io.ReadFull(r, trackData); err != nil {
			return nil, 0, err
		}

		trackEvents, err := parseTrack(trackData)
		if err != nil {
			continue
		}
		allEvents = append(allEvents, trackEvents...)
	}

	return allEvents, tpb, nil
}

func parseTrack(data []byte) ([]RawMidiEvent, error) {
	r := bytes.NewReader(data)
	var events []RawMidiEvent
	var currentTick int64
	var runningStatus byte

	for r.Len() > 0 {
		delta, err := readVarLength(r)
		if err != nil {
			break
		}
		currentTick += int64(delta)

		b, err := r.ReadByte()
		if err != nil {
			break
		}

		var status byte
		if b >= 0x80 {
			status = b
			if b < 0xF0 {
				runningStatus = b
			}
		} else {
			if runningStatus == 0 {
				continue
			}
			status = runningStatus
			_ = r.UnreadByte()
		}

		if status == 0xFF {
			// Meta Event
			metaType, err := r.ReadByte()
			if err != nil {
				break
			}
			metaLen, err := readVarLength(r)
			if err != nil {
				break
			}
			// 声明长度以轨内剩余字节为上界（readVarLength 已限 4 字节，
			// 但合法上限 0x0FFFFFFF 仍远超实际数据，先分配会 OOM）
			if metaLen > uint32(r.Len()) {
				break
			}
			metaData := make([]byte, metaLen)
			_, _ = io.ReadFull(r, metaData)
			if metaType == 0x2F { // End of track
				break
			}
		} else if status == 0xF0 || status == 0xF7 {
			// SysEx Event
			sysExLen, err := readVarLength(r)
			if err != nil {
				break
			}
			_, _ = io.CopyN(io.Discard, r, int64(sysExLen))
		} else {
			// Channel Event
			channel := int(status & 0x0F)
			cmd := status & 0xF0
			switch cmd {
			case 0x80: // Note Off
				note, _ := r.ReadByte()
				vel, _ := r.ReadByte()
				events = append(events, RawMidiEvent{
					AbsTick:  currentTick,
					MsgType:  "note_off",
					Channel:  channel,
					Note:     int(note),
					Velocity: int(vel),
				})
			case 0x90: // Note On
				note, _ := r.ReadByte()
				vel, _ := r.ReadByte()
				events = append(events, RawMidiEvent{
					AbsTick:  currentTick,
					MsgType:  "note_on",
					Channel:  channel,
					Note:     int(note),
					Velocity: int(vel),
				})
			case 0xA0, 0xB0, 0xE0: // 2 bytes payload
				_, _ = r.ReadByte()
				_, _ = r.ReadByte()
			case 0xC0, 0xD0: // 1 byte payload
				_, _ = r.ReadByte()
			}
		}
	}

	return events, nil
}

// readVarLength 读取 SMF 变长数（spec 上限 4 字节 / 0x0FFFFFFF）。
// 不限长度的话恶意文件可用连续 0x80 让循环空转到 EOF。
func readVarLength(r *bytes.Reader) (uint32, error) {
	var value uint32
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value = (value << 7) | uint32(b&0x7F)
		if (b & 0x80) == 0 {
			return value, nil
		}
	}
	return 0, errors.New("变长数超过 SMF 规范的 4 字节上限")
}

// GetNote 解析输入 MIDI 文件并返回 note_table 格式
func GetNote(filePath string, saveToFile bool) ([]string, error) {
	if filePath == "" {
		filePath = config.InputMidi
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("无法读取 MIDI 文件: %w", err)
	}

	outputList, err := ParseMidiToCustomFormat(data)
	if err != nil {
		return nil, err
	}

	if saveToFile && len(outputList) > 0 {
		_ = os.MkdirAll(config.DoingDir, 0755)
		content := ""
		for _, item := range outputList {
			content += item + "\n"
		}
		_ = os.WriteFile(config.DoingOutputTxt, []byte(content), 0644)
	}

	return outputList, nil
}
