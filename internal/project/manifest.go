package project

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// uniqueDstPath 目标已存在时在扩展名前插入 " (n)" 递增。os.Rename 在
// Windows 上会直接替换已存在的目标文件——恢复回收站时若不查重，会把
// 用户新建的同名文件无声覆盖销毁
func uniqueDstPath(dst string) string {
	if _, err := os.Stat(dst); err != nil {
		return dst // 目标不存在：直接用原名
	}
	ext := filepath.Ext(dst)
	base := strings.TrimSuffix(dst, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	// 极端拥挤场景兜底：时间戳后缀必然唯一
	return fmt.Sprintf("%s (%s)%s", base, time.Now().Format("150405.000"), ext)
}

// MoveToTrash 将文件移入项目回收站。返回实际落位的回收站相对路径
// （同名冲突时为唯一名），调用方以此作为回滚/恢复的凭据
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
	dst := uniqueDstPath(filepath.Join(trash, filepath.FromSlash(name)))
	_ = os.MkdirAll(filepath.Dir(dst), 0755)

	if err := os.Rename(src, dst); err != nil {
		slog.Error("移入回收站失败", "src", src, "err", err)
		return ""
	}

	rel, err := filepath.Rel(trash, dst)
	if err != nil {
		return name
	}
	return filepath.ToSlash(rel)
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
		dst := uniqueDstPath(filepath.Join(base, filepath.FromSlash(rel)))
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

// DefaultBPM 项目全局 BPM 默认值（新建工程 / 未设置时）
const DefaultBPM = 120

// GetProjectBPM 读取项目全局 BPM（存于 midi.json 根级 bpm 字段）。
// 未设置 / 非法 / 超出 30-300 时回退 DefaultBPM。
func GetProjectBPM(projectID string) int {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return DefaultBPM
	}
	mfile := filepath.Join(pdir, "midi.json")
	if data, err := os.ReadFile(mfile); err == nil {
		var root struct {
			BPM int `json:"bpm"`
		}
		if err := json.Unmarshal(data, &root); err == nil && root.BPM >= 30 && root.BPM <= 300 {
			return root.BPM
		}
	}
	return DefaultBPM
}

// SaveProjectBPM 写入项目全局 BPM，保留清单中已有的 midi_files。
// 与清单保存相同的 原子写（tmp+rename）策略。
func SaveProjectBPM(projectID string, bpm int) {
	if bpm < 30 || bpm > 300 {
		return
	}
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return
	}
	mfile := filepath.Join(pdir, "midi.json")

	root := map[string]any{"bpm": bpm}
	if data, err := os.ReadFile(mfile); err == nil {
		var existing map[string]json.RawMessage
		if err := json.Unmarshal(data, &existing); err == nil {
			if mf, ok := existing["midi_files"]; ok {
				root["midi_files"] = mf
			}
		}
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return
	}
	tmp := mfile + ".tmp"
	if err := os.WriteFile(tmp, out, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, mfile)
}

// SaveMidiManifest 保存项目 MIDI 清单（保留根级全局 bpm 字段）
func SaveMidiManifest(projectID string, files []MidiFileInfo) {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return
	}
	mfile := filepath.Join(pdir, "midi.json")
	tmp := mfile + ".tmp"

	root := map[string]any{"midi_files": files}
	if bpm := GetProjectBPM(projectID); bpm != DefaultBPM {
		root["bpm"] = bpm
	}
	data, err := json.MarshalIndent(root, "", "  ")
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
