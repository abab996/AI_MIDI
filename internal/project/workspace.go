package project

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var midiExts = map[string]bool{
	".mid":  true,
	".midi": true,
}

// GetWorkspaceDir 返回绑定的工作区目录
func GetWorkspaceDir(projectID string) string {
	meta, err := LoadProject(projectID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(meta.WorkspaceDir)
}

// SetWorkspaceDir 设置或清除工作区绑定
func SetWorkspaceDir(projectID, path string) {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return
	}

	metaFile := filepath.Join(pdir, "meta.json")
	var meta ProjectMeta
	if data, err := os.ReadFile(metaFile); err == nil {
		_ = json.Unmarshal(data, &meta)
	}

	cleanPath := strings.TrimSpace(path)
	if cleanPath != "" {
		meta.WorkspaceDir = cleanPath
	} else {
		meta.WorkspaceDir = ""
	}
	meta.UpdatedAt = nowISO()

	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(metaFile, metaData, 0644)

	updateIndexEntry(projectID, func(e *ProjectEntry) {
		e.UpdatedAt = meta.UpdatedAt
	})
}

// GetMidiBaseDir 获取主 MIDI 目录（绑定则为工作区，否则为 projects/<id>/midi）
func GetMidiBaseDir(projectID string) string {
	ws := GetWorkspaceDir(projectID)
	if ws != "" {
		return ws
	}
	return MidiDir(projectID)
}

// GetMidiMirrorDir 获取镜像目录（绑定时为 projects/<id>/midi，未绑定为 ""）
func GetMidiMirrorDir(projectID string) string {
	if GetWorkspaceDir(projectID) != "" {
		return MidiDir(projectID)
	}
	return ""
}

// ScanMidiFiles 递归扫描目录下的所有 MIDI 文件
func ScanMidiFiles(baseDir string) []MidiFileInfo {
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return nil
	}

	var result []MidiFileInfo
	_ = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if midiExts[ext] {
			rel, err := filepath.Rel(baseDir, path)
			if err == nil {
				fi, _ := d.Info()
				size := int64(0)
				if fi != nil {
					size = fi.Size()
				}
				result = append(result, MidiFileInfo{
					Name: filepath.ToSlash(rel),
					Path: path,
					Size: size,
				})
			}
		}
		return nil
	})

	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

// ScanDirs 递归扫描所有子目录相对路径
func ScanDirs(baseDir string) []string {
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return nil
	}

	var result []string
	_ = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == baseDir {
			return nil
		}
		if d.IsDir() {
			rel, err := filepath.Rel(baseDir, path)
			if err == nil {
				result = append(result, filepath.ToSlash(rel))
			}
		}
		return nil
	})

	sort.Strings(result)
	return result
}

func mergeNoteTable(scanned []MidiFileInfo, prev []MidiFileInfo) []MidiFileInfo {
	prevMap := make(map[string]string)
	for _, f := range prev {
		prevMap[f.Name] = f.NoteTable
	}
	for i := range scanned {
		if nt, ok := prevMap[scanned[i].Name]; ok {
			scanned[i].NoteTable = nt
		}
	}
	return scanned
}

// ScanMidiFilesKeepNotes 扫描 MIDI 文件并保留旧清单中的音符表
// （文件移动等重扫场景：全量 ScanMidiFiles 会把 note_table 清空）
func ScanMidiFilesKeepNotes(baseDir string, prev []MidiFileInfo) []MidiFileInfo {
	return mergeNoteTable(ScanMidiFiles(baseDir), prev)
}

// ValidateWorkspacePath 校验工作区目录安全性，禁止系统根目录与敏感系统目录
func ValidateWorkspacePath(wsPath string) error {
	clean, err := filepath.Abs(wsPath)
	if err != nil {
		return fmt.Errorf("无效的工作区路径: %w", err)
	}

	vol := filepath.VolumeName(clean)
	root := vol + string(filepath.Separator)
	if clean == root || clean == vol || clean == "/" {
		return fmt.Errorf("禁止将磁盘根目录绑定为工作区: %s", clean)
	}

	rel, _ := filepath.Rel(root, clean)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	firstSeg := ""
	if len(parts) > 0 {
		firstSeg = strings.ToLower(parts[0])
	}

	forbidden := map[string]bool{
		"windows": true, "system32": true, "program files": true,
		"program files (x86)": true, "programdata": true, "etc": true,
		"var": true, "bin": true, "sbin": true, "usr": true, "sys": true,
		"proc": true, "dev": true,
	}

	if forbidden[firstSeg] {
		return fmt.Errorf("禁止将系统关键目录绑定为工作区: %s", clean)
	}

	return nil
}

// BindWorkspace 绑定工作区并同步项目文件
func BindWorkspace(projectID, workspaceDir string) (map[string]any, error) {
	wsClean, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, err
	}
	if err := ValidateWorkspacePath(wsClean); err != nil {
		return nil, err
	}

	if fi, err := os.Stat(wsClean); err == nil && !fi.IsDir() {
		return nil, fmt.Errorf("路径已存在且不是目录: %s", wsClean)
	}
	_ = os.MkdirAll(wsClean, 0755)

	SetWorkspaceDir(projectID, wsClean)

	srcDir := MidiDir(projectID)
	var renamed [][2]string

	if _, err := os.Stat(srcDir); err == nil {
		_ = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if !midiExts[ext] {
				return nil
			}

			rel, _ := filepath.Rel(srcDir, path)
			dst := filepath.Join(wsClean, rel)
			if _, err := os.Stat(dst); err == nil {
				// 重名加序号
				stem := strings.TrimSuffix(filepath.Base(dst), ext)
				dir := filepath.Dir(dst)
				idx := 2
				for {
					cand := filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, idx, ext))
					if _, err := os.Stat(cand); os.IsNotExist(err) {
						dst = cand
						break
					}
					idx++
				}
				newRel, _ := filepath.Rel(wsClean, dst)
				renamed = append(renamed, [2]string{filepath.ToSlash(rel), filepath.ToSlash(newRel)})
			}

			_ = os.MkdirAll(filepath.Dir(dst), 0755)
			_ = copyFile(path, dst)
			return nil
		})
	}

	prevFiles := LoadMidiManifest(projectID)
	midiFiles := mergeNoteTable(ScanMidiFiles(wsClean), prevFiles)
	SaveMidiManifest(projectID, midiFiles)

	slog.Info("项目已绑定工作区", "id", projectID, "ws", wsClean, "renamed", len(renamed))
	return map[string]any{
		"renamed":    renamed,
		"midi_files": midiFiles,
	}, nil
}

// CopyFile 复制文件（公开，供 handler 调用）
func CopyFile(src, dst string) error {
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// UnbindWorkspace 解绑工作区
func UnbindWorkspace(projectID string) []MidiFileInfo {
	SetWorkspaceDir(projectID, "")
	prevFiles := LoadMidiManifest(projectID)
	base := MidiDir(projectID)
	midiFiles := mergeNoteTable(ScanMidiFiles(base), prevFiles)
	SaveMidiManifest(projectID, midiFiles)
	slog.Info("项目已解绑工作区", "id", projectID)
	return midiFiles
}

// SyncWorkspaceToProjects 以工作区为准完全同步到 projects 镜像
func SyncWorkspaceToProjects(projectID string, prevMidiFiles []MidiFileInfo) []MidiFileInfo {
	ws := GetWorkspaceDir(projectID)
	if ws == "" {
		return prevMidiFiles
	}

	mirror := MidiDir(projectID)
	_ = os.MkdirAll(mirror, 0755)

	wsFiles := ScanMidiFiles(ws)
	mirrorFiles := ScanMidiFiles(mirror)

	wsMap := make(map[string]MidiFileInfo)
	for _, f := range wsFiles {
		wsMap[f.Name] = f
	}

	mirrorMap := make(map[string]MidiFileInfo)
	for _, f := range mirrorFiles {
		mirrorMap[f.Name] = f
	}

	// 1. 工作区有，镜像无或已变更 -> 复制到镜像
	for rel, wf := range wsMap {
		dst := filepath.Join(mirror, filepath.FromSlash(rel))
		if mf, ok := mirrorMap[rel]; !ok || mf.Size != wf.Size {
			_ = os.MkdirAll(filepath.Dir(dst), 0755)
			_ = copyFile(wf.Path, dst)
		}
	}

	// 2. 镜像有，工作区无 -> 删除镜像中的多余文件
	for rel, mf := range mirrorMap {
		if _, ok := wsMap[rel]; !ok {
			_ = os.Remove(mf.Path)
		}
	}

	result := mergeNoteTable(wsFiles, prevMidiFiles)
	SaveMidiManifest(projectID, result)
	return result
}
