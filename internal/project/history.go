package project

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
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

	tmp := hfile + fmt.Sprintf(".tmp.%d.%d", os.Getpid(), time.Now().UnixNano())
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
		if uerr := json.Unmarshal(data, &root); uerr == nil {
			messages = root.Messages
		} else {
			// 损坏先备份再按空历史继续：否则下一次 SaveHistory 会把可能
			// 半截可恢复的原件无声覆盖掉（与 index.json 损坏策略一致）
			backup := fmt.Sprintf("%s.corrupt-%s-p%d-%d", hfile,
				time.Now().Format("20060102-150405"), os.Getpid(), corruptBackupSeq.Add(1))
			if rerr := os.Rename(hfile, backup); rerr == nil {
				slog.Error("对话历史损坏，已备份待人工恢复", "file", hfile, "backup", backup, "err", uerr)
			} else {
				slog.Error("对话历史损坏且备份失败", "file", hfile, "err", uerr, "renameErr", rerr)
			}
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

	tmp := efile + fmt.Sprintf(".tmp.%d.%d", os.Getpid(), time.Now().UnixNano())
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
	if uerr := json.Unmarshal(data, &root); uerr == nil {
		return root.EditHistory
	} else {
		// 同 LoadHistory：损坏先备份，防止下一次保存覆盖原件
		backup := fmt.Sprintf("%s.corrupt-%s-p%d-%d", efile,
			time.Now().Format("20060102-150405"), os.Getpid(), corruptBackupSeq.Add(1))
		if rerr := os.Rename(efile, backup); rerr == nil {
			slog.Error("编辑历史损坏，已备份待人工恢复", "file", efile, "backup", backup, "err", uerr)
		} else {
			slog.Error("编辑历史损坏且备份失败", "file", efile, "err", uerr, "renameErr", rerr)
		}
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
