package midi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// EmptyNoteTableError 当 AI 回复中未解析出任何音符数据时返回
var ErrEmptyNoteTable = errors.New("未从 AI 回复中解析出任何音符")

// NoteEvent 表示单个音符事件
type NoteEvent struct {
	Note     string  `json:"note"`
	Velocity int     `json:"velocity"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
}

var (
	reNote  = regexp.MustCompile(`(?i)note:\s*[^A-G]*([A-G][#b]*-?\d+)`)
	reVel   = regexp.MustCompile(`(?i)velocity:\s*[^\d.-]*([-\d.]+)`)
	reStart = regexp.MustCompile(`(?i)start:\s*[^\d.-]*([-\d.]+)`)
	reEnd   = regexp.MustCompile(`(?i)end:\s*[^\d.-]*([-\d.]+)`)
)

// ExtractFields 从单行文本中宽容提取音符字段
func ExtractFields(line string) (*NoteEvent, bool) {
	noteMatch := reNote.FindStringSubmatch(line)
	velMatch := reVel.FindStringSubmatch(line)
	startMatch := reStart.FindStringSubmatch(line)
	endMatch := reEnd.FindStringSubmatch(line)

	if len(noteMatch) < 2 || len(velMatch) < 2 || len(startMatch) < 2 || len(endMatch) < 2 {
		return nil, false
	}

	velVal, err := strconv.ParseFloat(velMatch[1], 64)
	if err != nil {
		return nil, false
	}
	startVal, err := strconv.ParseFloat(startMatch[1], 64)
	if err != nil {
		return nil, false
	}
	endVal, err := strconv.ParseFloat(endMatch[1], 64)
	if err != nil {
		return nil, false
	}

	if startVal < 0 {
		startVal = 0
	}
	if endVal < startVal {
		return nil, false
	}

	return &NoteEvent{
		Note:     noteMatch[1],
		Velocity: int(math.Round(velVal)),
		Start:    startVal,
		End:      endVal,
	}, true
}

// FormatNoteEvent 将 NoteEvent 格式化为 note_table 模板字符串
func FormatNoteEvent(n NoteEvent) string {
	formatFloat := func(f float64) string {
		// 消除尾部多余的 0 和点
		s := strconv.FormatFloat(f, 'f', -1, 64)
		return s
	}
	return fmt.Sprintf(`[note: "%s", velocity: "%d", start: "%s", end: "%s"]`,
		n.Note, n.Velocity, formatFloat(n.Start), formatFloat(n.End))
}

// TryParseJSONNotes 尝试从 JSON 数组文本中解析音符列表
func TryParseJSONNotes(text string) ([]NoteEvent, bool) {
	clean := strings.TrimSpace(text)
	if strings.HasPrefix(clean, "```") {
		lines := strings.Split(clean, "\n")
		if len(lines) >= 2 {
			if strings.HasPrefix(lines[0], "```") {
				lines = lines[1:]
			}
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			clean = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}

	type rawJSONNote struct {
		Note     string `json:"note"`
		Velocity any    `json:"velocity"`
		Start    any    `json:"start"`
		End      any    `json:"end"`
	}

	parseAnyFloat := func(v any) (float64, bool) {
		if v == nil {
			return 0, false
		}
		switch val := v.(type) {
		case float64:
			return val, true
		case int:
			return float64(val), true
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
			return f, err == nil
		}
		return 0, false
	}

	var list []rawJSONNote
	err := json.Unmarshal([]byte(clean), &list)
	if err != nil {
		var obj struct {
			Notes []rawJSONNote `json:"notes"`
		}
		if err2 := json.Unmarshal([]byte(clean), &obj); err2 == nil && len(obj.Notes) > 0 {
			list = obj.Notes
		}
	}

	if len(list) == 0 {
		return nil, false
	}

	var notes []NoteEvent
	for _, item := range list {
		note := strings.TrimSpace(item.Note)
		if note == "" {
			continue
		}
		velF, okV := parseAnyFloat(item.Velocity)
		startF, okS := parseAnyFloat(item.Start)
		endF, okE := parseAnyFloat(item.End)
		if !okS || !okE {
			continue
		}
		if !okV {
			velF = 80
		}
		if startF < 0 {
			startF = 0
		}
		if endF < startF {
			continue
		}
		notes = append(notes, NoteEvent{
			Note:     note,
			Velocity: int(math.Round(velF)),
			Start:    startF,
			End:      endF,
		})
	}

	if len(notes) > 0 {
		return notes, true
	}
	return nil, false
}

// ParseNoteTableLines 从文本内容中解析出音符列表（同时支持自定义 note_table 文本与 JSON 格式）
func ParseNoteTableLines(text string) ([]NoteEvent, error) {
	if jsonNotes, ok := TryParseJSONNotes(text); ok && len(jsonNotes) > 0 {
		return jsonNotes, nil
	}

	var notes []NoteEvent
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if event, ok := ExtractFields(line); ok {
			notes = append(notes, *event)
		}
	}

	if len(notes) == 0 {
		return nil, ErrEmptyNoteTable
	}
	return notes, nil
}
