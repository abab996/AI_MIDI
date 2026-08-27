package project

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// MidiFileInfo 项目文件元数据
type MidiFileInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	NoteTable string `json:"note_table,omitempty"`
}

const TrashDirname = ".trash"

// TrashDir 获取项目回收站路径
func TrashDir(projectID string) string {
	pdir, _ := ProjectDir(projectID)
	return filepath.Join(pdir, TrashDirname)
}

// ClearTrash 清空项目回收站
func ClearTrash(projectID string) {
	trash := TrashDir(projectID)
	_ = os.RemoveAll(trash)
}

// MoveToTrash 将文件移入项目回收站
func MoveToTrash(projectID string, fileInfo MidiFileInfo) string {
	src := fileInfo.Path
	name := fileInfo.Name
	if name == "" {
		return ""
	}
	if src == "" {
		src = filepath.Join(GetMidiBaseDir(projectID), filepath.FromSlash(name))
	}
	if fi, err := os.Stat(src); err != nil || fi.IsDir() {
		return ""
	}

	trash := TrashDir(projectID)
	dst := filepath.Join(trash, filepath.FromSlash(name))
	_ = os.MkdirAll(filepath.Dir(dst), 0755)

	if err := os.Rename(src, dst); err != nil {
		slog.Error("移入回收站失败", "src", src, "err", err)
		return ""
	}

	return name
}

// RestoreFromTrash 从回收站恢复文件至主目录
func RestoreFromTrash(projectID string, trashRels []string) {
	base := GetMidiBaseDir(projectID)
	trash := TrashDir(projectID)

	for _, rel := range trashRels {
		if rel == "" {
			continue
		}
		src := filepath.Join(trash, filepath.FromSlash(rel))
		if fi, err := os.Stat(src); err != nil || fi.IsDir() {
			continue
		}
		dst := filepath.Join(base, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(dst), 0755)

		if err := os.Rename(src, dst); err != nil {
			slog.Error("从回收站恢复失败", "rel", rel, "err", err)
		}
	}

	// 若回收站为空则清理
	cleanupEmptyParents(trash, filepath.Dir(trash))
}

func cleanupEmptyParents(startPath, stopDir string) {
	stopClean := filepath.Clean(stopDir)
	curr := filepath.Clean(startPath)

	for curr != stopClean && curr != "." && curr != "/" && curr != "\\" {
		entries, err := os.ReadDir(curr)
		if err != nil || len(entries) > 0 {
			break
		}
		_ = os.Remove(curr)
		curr = filepath.Dir(curr)
	}
}

// RemoveEmptyDirs 删除指定文件夹及其所有空子目录（自底向上）。
// 与 Python _on_delete_folder 的空目录清理一致：文件已移入回收站后，
// 剩下的空目录逐级 rmdir，撤销时 trash 恢复会自动重建层级。
func RemoveEmptyDirs(baseDir, folderRel string) error {
	target := filepath.Join(baseDir, filepath.FromSlash(folderRel))
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return nil
	}
	removeEmptyDirsRecursive(target, filepath.Clean(baseDir))
	return nil
}

func removeEmptyDirsRecursive(dir, stopDir string) {
	if filepath.Clean(dir) == filepath.Clean(stopDir) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// 先递归处理子目录
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirsRecursive(filepath.Join(dir, e.Name()), stopDir)
		}
	}
	// 重新检查：子目录处理后当前目录可能已空
	entries, err = os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// SaveMidiManifest 保存项目 MIDI 清单
func SaveMidiManifest(projectID string, files []MidiFileInfo) {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return
	}
	mfile := filepath.Join(pdir, "midi.json")
	tmp := mfile + ".tmp"

	data, err := json.MarshalIndent(map[string]any{"midi_files": files}, "", "  ")
	if err != nil {
		return
	}

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, mfile)
}

// LoadMidiManifest 读取项目 MIDI 清单
func LoadMidiManifest(projectID string) []MidiFileInfo {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return nil
	}

	mfile := filepath.Join(pdir, "midi.json")
	if data, err := os.ReadFile(mfile); err == nil {
		var root struct {
			MidiFiles []MidiFileInfo `json:"midi_files"`
		}
		if err := json.Unmarshal(data, &root); err == nil {
			return root.MidiFiles
		}
	}

	// 兼容旧版 history.json 内嵌的 midi_files
	hfile := filepath.Join(pdir, "history.json")
	if data, err := os.ReadFile(hfile); err == nil {
		var root struct {
			MidiFiles []MidiFileInfo `json:"midi_files"`
		}
		if err := json.Unmarshal(data, &root); err == nil && len(root.MidiFiles) > 0 {
			return root.MidiFiles
		}
	}

	return nil
}
