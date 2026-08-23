package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aimidi/internal/midi"
)

// SafeJoin 在指定根目录下安全解析相对路径，防止路径穿越
func SafeJoin(baseDir, filename string) (string, error) {
	cleanName := strings.TrimSpace(filename)
	if cleanName == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	if strings.Contains(cleanName, "\x00") {
		return "", fmt.Errorf("路径包含非法空字节")
	}
	if filepath.IsAbs(cleanName) {
		return "", fmt.Errorf("路径不能是绝对路径: %s", cleanName)
	}

	baseClean := filepath.Clean(baseDir)
	target := filepath.Join(baseClean, cleanName)
	targetClean := filepath.Clean(target)

	if targetClean == baseClean {
		return "", fmt.Errorf("路径不能指向根目录本身")
	}

	rel, err := filepath.Rel(baseClean, targetClean)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("路径逃逸检测: %s", cleanName)
	}

	return targetClean, nil
}

func mirrorPath(mainPath, mainBase, mirrorBase string) string {
	if mirrorBase == "" {
		return ""
	}
	rel, err := filepath.Rel(filepath.Clean(mainBase), filepath.Clean(mainPath))
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.Join(mirrorBase, rel)
}

func writeWithMirror(mainPath, mainBase, mirrorBase string) {
	mp := mirrorPath(mainPath, mainBase, mirrorBase)
	if mp == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(mp), 0755)
	src, err := os.Open(mainPath)
	if err != nil {
		return
	}
	defer src.Close()

	dst, err := os.Create(mp)
	if err != nil {
		return
	}
	defer dst.Close()
	_, _ = io.Copy(dst, src)
}

func cleanupEmptyParents(startPath, rootDir string) {
	rootClean := filepath.Clean(rootDir)
	curr := filepath.Clean(startPath)

	for curr != rootClean && curr != "." && curr != "/" && curr != "\\" {
		entries, err := os.ReadDir(curr)
		if err != nil || len(entries) > 0 {
			break
		}
		_ = os.Remove(curr)
		curr = filepath.Dir(curr)
	}
}

func deleteWithMirror(mainPath, mainBase, mirrorBase string) {
	_ = os.Remove(mainPath)
	cleanupEmptyParents(filepath.Dir(mainPath), mainBase)

	mp := mirrorPath(mainPath, mainBase, mirrorBase)
	if mp != "" {
		_ = os.Remove(mp)
		cleanupEmptyParents(filepath.Dir(mp), mirrorBase)
	}
}

// ExecuteTool 执行指定的 MCP 工具调用并返回结果文本
func ExecuteTool(name string, rawArgs map[string]any, baseDir, mirrorDir string) (string, error) {
	switch name {
	case "read_library_file":
		fn, _ := rawArgs["filename"].(string)
		content, err := ReadLibraryFile(fn)
		if err != nil {
			return fmt.Sprintf("错误：%v", err), nil
		}
		return content, nil

	case "list_midi_files":
		return executeListMidiFiles(baseDir), nil

	case "parse_midi":
		fn, _ := rawArgs["filename"].(string)
		path, err := SafeJoin(baseDir, fn)
		if err != nil {
			return fmt.Sprintf("错误：%v", err), nil
		}
		notes, err := midi.GetNote(path, false)
		if err != nil {
			return fmt.Sprintf("错误：解析失败 — %v", err), nil
		}
		if len(notes) == 0 {
			return "错误：未能从文件中解析出任何音符。", nil
		}
		return strings.Join(notes, "\n"), nil

	case "create_midi":
		fn, _ := rawArgs["filename"].(string)
		if fn == "" {
			fn = "output.mid"
		}
		bpm := 120
		if b, ok := rawArgs["bpm"].(float64); ok {
			bpm = int(b)
		} else if b, ok := rawArgs["bpm"].(int); ok {
			bpm = b
		}

		notesStr, _ := rawArgs["notes"].(string)
		if notesStr == "" {
			notesStr, _ = rawArgs["note_table"].(string)
		}
		if notesStr == "" {
			return "错误：缺少 notes 参数，请提供 note_table 格式的音符数据", nil
		}

		notesStr = midi.NormalizeNoteData(notesStr)

		targetPath, err := SafeJoin(baseDir, fn)
		if err != nil {
			return fmt.Sprintf("错误：%v", err), nil
		}

		_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
		midiBytes, err := midi.TxtToMidi(notesStr, targetPath, bpm)
		if err != nil {
			return fmt.Sprintf("错误：创建失败 — %v", err), nil
		}

		writeWithMirror(targetPath, baseDir, mirrorDir)

		noteCount := len(strings.Split(strings.TrimSpace(notesStr), "\n"))
		sizeKB := len(midiBytes) / 1024
		if sizeKB < 1 {
			sizeKB = 1
		}
		return fmt.Sprintf("成功创建 %s：%d 个音符，BPM %d，%d KB", filepath.ToSlash(fn), noteCount, bpm, sizeKB), nil

	case "delete_midi":
		fn, _ := rawArgs["filename"].(string)
		targetPath, err := SafeJoin(baseDir, fn)
		if err != nil {
			return fmt.Sprintf("错误：%v", err), nil
		}
		if _, err := os.Stat(targetPath); os.IsNotExist(err) {
			return fmt.Sprintf("错误：文件不存在 — %s", fn), nil
		}
		ext := strings.ToLower(filepath.Ext(targetPath))
		if ext != ".mid" && ext != ".midi" {
			return fmt.Sprintf("错误：只能删除 MIDI 文件 (.mid / .midi) — %s", fn), nil
		}

		deleteWithMirror(targetPath, baseDir, mirrorDir)
		return fmt.Sprintf("成功删除 %s", filepath.ToSlash(fn)), nil

	case "create_folder":
		fn, _ := rawArgs["name"].(string)
		fn = strings.TrimSpace(fn)
		if fn == "" {
			return "错误：文件夹名不能为空", nil
		}
		targetPath, err := SafeJoin(baseDir, fn)
		if err != nil {
			return fmt.Sprintf("错误：%v", err), nil
		}
		if fi, err := os.Stat(targetPath); err == nil {
			if fi.IsDir() {
				return fmt.Sprintf("文件夹已存在: %s", filepath.ToSlash(fn)), nil
			}
			return fmt.Sprintf("错误：路径已存在且不是文件夹 - %s", filepath.ToSlash(fn)), nil
		}

		if err := os.MkdirAll(targetPath, 0755); err != nil {
			return fmt.Sprintf("错误：创建文件夹失败 - %v", err), nil
		}
		mp := mirrorPath(targetPath, baseDir, mirrorDir)
		if mp != "" {
			_ = os.MkdirAll(mp, 0755)
		}
		return fmt.Sprintf("成功创建文件夹: %s", filepath.ToSlash(fn)), nil

	case "list_project_structure":
		return executeListProjectStructure(baseDir), nil

	default:
		return fmt.Sprintf("未知工具：%s", name), nil
	}
}

func executeListMidiFiles(baseDir string) string {
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return "（output 目录不存在）"
	}

	var lines []string
	_ = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".mid" || ext == ".midi" {
			rel, err := filepath.Rel(baseDir, path)
			if err == nil {
				fi, _ := d.Info()
				sizeKB := int64(1)
				if fi != nil && fi.Size()/1024 > 1 {
					sizeKB = fi.Size() / 1024
				}
				lines = append(lines, fmt.Sprintf("%s  (%d KB)", filepath.ToSlash(rel), sizeKB))
			}
		}
		return nil
	})

	if len(lines) == 0 {
		return "（暂无 MIDI 文件）"
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

type treeNode struct {
	name     string
	isDir    bool
	children map[string]*treeNode
}

func executeListProjectStructure(baseDir string) string {
	baseDirClean := filepath.Clean(baseDir)
	baseName := filepath.Base(baseDirClean)
	if _, err := os.Stat(baseDirClean); os.IsNotExist(err) {
		return "（output 目录不存在）"
	}

	root := &treeNode{name: baseName, isDir: true, children: make(map[string]*treeNode)}
	count := 0

	_ = filepath.WalkDir(baseDirClean, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == baseDirClean {
			return nil
		}
		rel, err := filepath.Rel(baseDirClean, path)
		if err != nil {
			return nil
		}

		if d.IsDir() {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			curr := root
			for _, part := range parts {
				if _, ok := curr.children[part]; !ok {
					curr.children[part] = &treeNode{name: part, isDir: true, children: make(map[string]*treeNode)}
				}
				curr = curr.children[part]
			}
		} else {
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".mid" || ext == ".midi" {
				count++
				parts := strings.Split(filepath.ToSlash(rel), "/")
				curr := root
				for i, part := range parts {
					if i == len(parts)-1 {
						curr.children[part] = &treeNode{name: part, isDir: false}
					} else {
						if _, ok := curr.children[part]; !ok {
							curr.children[part] = &treeNode{name: part, isDir: true, children: make(map[string]*treeNode)}
						}
						curr = curr.children[part]
					}
				}
			}
		}
		return nil
	})

	if count == 0 {
		return fmt.Sprintf("%s/\n（暂无 MIDI 文件）", baseName)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("%s/", baseName))

	var render func(n *treeNode, prefix string)
	render = func(n *treeNode, prefix string) {
		var dirKeys, fileKeys []string
		for k, child := range n.children {
			if child.isDir {
				dirKeys = append(dirKeys, k)
			} else {
				fileKeys = append(fileKeys, k)
			}
		}
		sort.Strings(dirKeys)
		sort.Strings(fileKeys)

		var allKeys []string
		allKeys = append(allKeys, dirKeys...)
		allKeys = append(allKeys, fileKeys...)

		for i, k := range allKeys {
			child := n.children[k]
			isLast := i == len(allKeys)-1
			connector := "├── "
			if isLast {
				connector = "└── "
			}

			lines = append(lines, fmt.Sprintf("%s%s%s", prefix, connector, child.name))
			if child.isDir {
				ext := "│   "
				if isLast {
					ext = "    "
				}
				render(child, prefix+ext)
			}
		}
	}

	render(root, "")
	return strings.Join(lines, "\n")
}

// ParseRawArgs 解析 JSON 格式参数
func ParseRawArgs(args string) map[string]any {
	res := make(map[string]any)
	if strings.TrimSpace(args) == "" {
		return res
	}
	_ = json.Unmarshal([]byte(args), &res)
	return res
}
