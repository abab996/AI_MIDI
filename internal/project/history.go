package project

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// HistoryFile 任务对话历史文件路径
func HistoryFile(projectID, taskID string, legacy bool) string {
	pdir, _ := ProjectDir(projectID)
	if legacy || taskID == "" {
		return filepath.Join(pdir, "history.json")
	}
	return filepath.Join(pdir, "tasks", taskID, "history.json")
}

// EditHistoryFile 修改历史快照文件路径
func EditHistoryFile(projectID, taskID string, legacy bool) string {
	pdir, _ := ProjectDir(projectID)
	if legacy || taskID == "" {
		return filepath.Join(pdir, "edit_history.json")
	}
	return filepath.Join(pdir, "tasks", taskID, "edit_history.json")
}

// SaveHistory 持久化对话历史与 MIDI 文件清单
func SaveHistory(projectID string, messages []map[string]any, midiFiles []MidiFileInfo, taskID string, legacy bool) {
	if taskID == "" {
		legacy = true
	}

	hfile := HistoryFile(projectID, taskID, legacy)
	_ = os.MkdirAll(filepath.Dir(hfile), 0755)

	payload := map[string]any{
		"messages": messages,
	}
	if legacy {
		payload["midi_files"] = midiFiles
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		slog.Error("序列化历史消息失败", "err", err)
		return
	}

	tmp := hfile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		_ = os.Rename(tmp, hfile)
	}

	// 项目级 MIDI 清单落盘
	SaveMidiManifest(projectID, midiFiles)

	// 更新索引
	now := nowISO()
	updateIndexEntry(projectID, func(e *ProjectEntry) {
		e.UpdatedAt = now
		e.MessageCount = len(messages)
		e.MidiCount = len(midiFiles)
	})
}

// LoadHistory 加载对话历史与 MIDI 文件清单
func LoadHistory(projectID, taskID string, legacy bool) ([]map[string]any, []MidiFileInfo) {
	if taskID == "" {
		legacy = true
	}

	hfile := HistoryFile(projectID, taskID, legacy)
	var messages []map[string]any

	if data, err := os.ReadFile(hfile); err == nil {
		var root struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(data, &root); err == nil {
			messages = root.Messages
		}
	}

	midiFiles := LoadMidiManifest(projectID)
	return messages, midiFiles
}

// SaveEditHistory 保存任务的修改历史快照
func SaveEditHistory(projectID string, history []map[string]any, taskID string, legacy bool) {
	if taskID == "" {
		legacy = true
	}
	efile := EditHistoryFile(projectID, taskID, legacy)
	_ = os.MkdirAll(filepath.Dir(efile), 0755)

	payload := map[string]any{"edit_history": history}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}

	tmp := efile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		_ = os.Rename(tmp, efile)
	}
}

// LoadEditHistory 加载任务的修改历史快照
func LoadEditHistory(projectID, taskID string, legacy bool) []map[string]any {
	if taskID == "" {
		legacy = true
	}
	efile := EditHistoryFile(projectID, taskID, legacy)
	data, err := os.ReadFile(efile)
	if err != nil {
		return nil
	}

	var root struct {
		EditHistory []map[string]any `json:"edit_history"`
	}
	if err := json.Unmarshal(data, &root); err == nil {
		return root.EditHistory
	}
	return nil
}

// DeleteTaskHistory 删除任务的对话历史与编辑快照
func DeleteTaskHistory(projectID, taskID string, legacy bool) {
	if legacy || taskID == "" {
		pdir, _ := ProjectDir(projectID)
		_ = os.Remove(filepath.Join(pdir, "history.json"))
		_ = os.Remove(filepath.Join(pdir, "edit_history.json"))
		return
	}
	pdir, _ := ProjectDir(projectID)
	_ = os.RemoveAll(filepath.Join(pdir, "tasks", taskID))
}
