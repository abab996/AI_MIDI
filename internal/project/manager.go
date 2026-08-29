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
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"aimidi/internal/config"
)

// corruptBackupSeq 损坏索引备份名的进程内序号（见 loadIndexLocked）
var corruptBackupSeq atomic.Uint32

var (
	indexLock sync.Mutex
	/* 索引内存镜像：TaskList 每 2s 轮询都会触发 ListProjects，此前每次
	   都读盘+JSON 解析。镜像与磁盘由 saveIndexLocked 写穿同步。 */
	indexCache []ProjectEntry
)

// ProjectEntry index.json 中的条目
type ProjectEntry struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	MessageCount int    `json:"message_count"`
	MidiCount    int    `json:"midi_count"`
}

// ProjectMeta meta.json 中的元数据
type ProjectMeta struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	WorkspaceDir string `json:"workspace_dir,omitempty"`
}

func ensureProjectsDir() {
	_ = os.MkdirAll(config.ProjectsDir, 0755)
}

func nowISO() string {
	return time.Now().Format("2006-01-02T15:04:05")
}

func getIndexFile() string {
	return filepath.Join(config.ProjectsDir, "index.json")
}

// ResetProjectsIndexCache 清空内存索引镜像。测试切换 config.ProjectsDir
// 后必须调用，否则旧目录的条目会被写进新目录的 index.json。
func ResetProjectsIndexCache() {
	indexLock.Lock()
	defer indexLock.Unlock()
	indexCache = nil
}

// loadIndexLocked 读取项目索引（调用方必须已持有 indexLock）。
// 文件损坏时备份为 index.json.corrupt-<时间戳> 并返回错误：调用方
// 不得基于空条目落盘覆盖，否则一次崩溃截断就会把所有项目从索引中抹掉
func loadIndexLocked() ([]ProjectEntry, error) {
	if indexCache != nil {
		return indexCache, nil
	}
	idxFile := getIndexFile()
	data, err := os.ReadFile(idxFile)
	if err != nil {
		return nil, nil // 文件缺失（首启）：不缓存，首次创建后自然填充
	}
	var root struct {
		Projects []ProjectEntry `json:"projects"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		// 备份名带进程内原子序号：同毫秒连续损坏时纯时间戳会撞名，
		// 后一次 rename 覆盖前一次备份（Linux CI 实测踩中过）
		backup := fmt.Sprintf("%s.corrupt-%s-p%d-%d", idxFile,
			time.Now().Format("20060102-150405"), os.Getpid(), corruptBackupSeq.Add(1))
		if rerr := os.Rename(idxFile, backup); rerr == nil {
			slog.Error("index.json 损坏，已备份待人工恢复；本次操作跳过索引写入", "backup", backup, "err", err)
		} else {
			slog.Error("index.json 损坏且备份失败，本次操作跳过索引写入", "err", err, "renameErr", rerr)
		}
		return nil, err
	}
	indexCache = root.Projects
	return indexCache, nil
}

func saveIndexLocked(entries []ProjectEntry) error {
	ensureProjectsDir()
	idxFile := getIndexFile()
	tmp := idxFile + ".tmp"

	root := map[string]any{"projects": entries}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}

	if err := os.Rename(tmp, idxFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	indexCache = entries // 写穿：磁盘与镜像保持一致
	return nil
}

func updateIndexEntry(projectID string, updateFn func(e *ProjectEntry)) {
	indexLock.Lock()
	defer indexLock.Unlock()

	entries, ierr := loadIndexLocked()
	if ierr != nil {
		return // 索引损坏：绝不基于空条目落盘（损坏原件已备份）
	}
	for i := range entries {
		if entries[i].ID == projectID {
			updateFn(&entries[i])
			break
		}
	}
	_ = saveIndexLocked(entries)
}

// ProjectDir 获取项目目录路径
func ProjectDir(projectID string) (string, error) {
	if projectID == "" || strings.Contains(projectID, "..") || strings.ContainsAny(projectID, "/\\\x00") {
		return "", fmt.Errorf("非法的项目 ID: %s", projectID)
	}
	return filepath.Join(config.ProjectsDir, projectID), nil
}

// MidiDir 获取项目的内部 midi/ 目录路径
func MidiDir(projectID string) string {
	pdir, _ := ProjectDir(projectID)
	return filepath.Join(pdir, "midi")
}

// ResolveMidiPath 解析并校验项目 MIDI 文件的绝对路径
func ResolveMidiPath(projectID, filename string) (string, error) {
	cleanName := strings.TrimSpace(filename)
	if cleanName == "" {
		return "", fmt.Errorf("filename 不能为空")
	}
	if strings.Contains(cleanName, "\x00") {
		return "", fmt.Errorf("filename 包含非法空字节")
	}
	if strings.Contains(cleanName, "..") {
		return "", fmt.Errorf("filename 包含非法路径段: %s", filename)
	}
	if strings.Contains(cleanName, "~") {
		return "", fmt.Errorf("filename 包含非法字符 '~': %s", filename)
	}
	if filepath.IsAbs(cleanName) {
		return "", fmt.Errorf("filename 不能是绝对路径: %s", filename)
	}

	baseDir := GetMidiBaseDir(projectID)
	target := filepath.Join(baseDir, cleanName)
	targetClean := filepath.Clean(target)
	baseClean := filepath.Clean(baseDir)

	if targetClean != baseClean {
		rel, err := filepath.Rel(baseClean, targetClean)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("路径逃逸检测: %s", filename)
		}
	}

	return targetClean, nil
}

// CreateProject 创建新项目
func CreateProject(name string) (ProjectMeta, error) {
	indexLock.Lock()
	defer indexLock.Unlock()

	ensureProjectsDir()
	now := nowISO()
	pid := strings.ReplaceAll(uuid.New().String(), "-", "")[:12]

	meta := ProjectMeta{
		ID:        pid,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}

	pdir := filepath.Join(config.ProjectsDir, pid)
	_ = os.MkdirAll(filepath.Join(pdir, "midi"), 0755)

	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(pdir, "meta.json"), metaData, 0644)

	histData, _ := json.MarshalIndent(map[string]any{"messages": []any{}, "midi_files": []any{}}, "", "  ")
	_ = os.WriteFile(filepath.Join(pdir, "history.json"), histData, 0644)

	entries, ierr := loadIndexLocked()
	if ierr != nil {
		slog.Warn("项目索引损坏，本次创建未登记索引（项目目录已生成，损坏原件已备份）", "id", pid)
	} else {
		entries = append(entries, ProjectEntry{
			ID:           pid,
			Name:         name,
			CreatedAt:    now,
			UpdatedAt:    now,
			MessageCount: 0,
			MidiCount:    0,
		})
		_ = saveIndexLocked(entries)
	}

	slog.Info("项目已创建", "name", name, "id", pid)
	return meta, nil
}

// DeleteProject 删除项目
func DeleteProject(projectID string) error {
	indexLock.Lock()
	defer indexLock.Unlock()

	entries, ierr := loadIndexLocked()
	if ierr != nil {
		slog.Warn("项目索引损坏，本次删除仅移除项目目录，索引待人工恢复", "id", projectID)
	} else {
		var newEntries []ProjectEntry
		for _, e := range entries {
			if e.ID != projectID {
				newEntries = append(newEntries, e)
			}
		}
		_ = saveIndexLocked(newEntries)
	}

	pdir, err := ProjectDir(projectID)
	if err == nil {
		_ = os.RemoveAll(pdir)
	}

	slog.Info("项目已删除", "id", projectID)
	return nil
}

// RenameProject 重命名项目
func RenameProject(projectID, newName string) error {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return err
	}

	metaFile := filepath.Join(pdir, "meta.json")
	var meta ProjectMeta
	if data, err := os.ReadFile(metaFile); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	meta.Name = newName
	meta.UpdatedAt = nowISO()
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(metaFile, metaData, 0644)

	updateIndexEntry(projectID, func(e *ProjectEntry) {
		e.Name = newName
		e.UpdatedAt = meta.UpdatedAt
	})

	slog.Info("项目已重命名", "id", projectID, "newName", newName)
	return nil
}

// CopyProject 深拷贝项目
func CopyProject(sourceID, newName string) (ProjectMeta, error) {
	srcDir, err := ProjectDir(sourceID)
	if err != nil {
		return ProjectMeta{}, err
	}
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return ProjectMeta{}, fmt.Errorf("源项目不存在: %s", sourceID)
	}

	newID := strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	now := nowISO()
	dstDir, _ := ProjectDir(newID)

	if err := copyDir(srcDir, dstDir); err != nil {
		return ProjectMeta{}, fmt.Errorf("复制项目目录失败: %w", err)
	}

	// 规范化并重写路径
	rewriteProjectPaths(dstDir, srcDir, newID)

	meta := ProjectMeta{
		ID:        newID,
		Name:      newName,
		CreatedAt: now,
		UpdatedAt: now,
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(dstDir, "meta.json"), metaData, 0644)

	indexLock.Lock()
	entries, ierr := loadIndexLocked()
	var srcMsgCount, srcMidiCount int
	for _, e := range entries {
		if e.ID == sourceID {
			srcMsgCount = e.MessageCount
			srcMidiCount = e.MidiCount
			break
		}
	}

	if ierr != nil {
		slog.Warn("项目索引损坏，本次复制未登记索引（项目目录已生成，损坏原件已备份）", "newID", newID)
	} else {
		entries = append(entries, ProjectEntry{
			ID:           newID,
			Name:         newName,
			CreatedAt:    now,
			UpdatedAt:    now,
			MessageCount: srcMsgCount,
			MidiCount:    srcMidiCount,
		})
		_ = saveIndexLocked(entries)
	}
	indexLock.Unlock()

	slog.Info("项目已复制", "source", sourceID, "newID", newID, "name", newName)
	return meta, nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		dstFile, err := os.Create(target)
		if err != nil {
			return err
		}
		defer dstFile.Close()
		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

func rewriteProjectPaths(dstDir, srcDir, newID string) {
	oldID := filepath.Base(srcDir)
	files := []string{"history.json", "midi.json"}

	for _, name := range files {
		target := filepath.Join(dstDir, name)
		data, err := os.ReadFile(target)
		if err != nil {
			continue
		}

		var root map[string]any
		if err := json.Unmarshal(data, &root); err != nil {
			continue
		}

		if midiFiles, ok := root["midi_files"].([]any); ok {
			base := filepath.Join(dstDir, "midi")
			for _, item := range midiFiles {
				if mf, ok := item.(map[string]any); ok {
					if mfName, ok := mf["name"].(string); ok && mfName != "" {
						mf["path"] = filepath.Join(base, filepath.FromSlash(mfName))
					} else if oldPath, ok := mf["path"].(string); ok && oldPath != "" {
						newPath := strings.ReplaceAll(oldPath, srcDir, dstDir)
						newPath = strings.ReplaceAll(newPath, oldID, newID)
						mf["path"] = newPath
					}
				}
			}
		}

		outData, _ := json.MarshalIndent(root, "", "  ")
		_ = os.WriteFile(target, outData, 0644)
	}
}

// ListProjects 返回按更新时间降序排列的项目列表。
// 返回缓存副本：调用方可能长期持有/改动返回值，不得污染内存镜像。
func ListProjects() []ProjectEntry {
	indexLock.Lock()
	defer indexLock.Unlock()

	entries, _ := loadIndexLocked() // 损坏时返回空列表（load 内部已备份并记日志），只读路径不落盘
	out := make([]ProjectEntry, len(entries))
	copy(out, entries)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

// LoadProject 读取指定项目的 meta.json
func LoadProject(projectID string) (ProjectMeta, error) {
	pdir, err := ProjectDir(projectID)
	if err != nil {
		return ProjectMeta{}, err
	}

	metaFile := filepath.Join(pdir, "meta.json")
	data, err := os.ReadFile(metaFile)
	if err != nil {
		return ProjectMeta{}, err
	}

	var meta ProjectMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return ProjectMeta{}, err
	}
	return meta, nil
}

// SearchProjects 全文搜索项目名及对话内容
/* 搜索内容缓存：搜索框 250ms 防抖仍会逐键触发全量扫描，此前每次请求
   都把所有项目的 history.json 读盘+解析。按文件缓存消息列表（mtime/size
   失效），写盘端 SaveHistory 更新 mtime 后自动重读。 */
type searchCacheEntry struct {
	modTime time.Time
	size    int64
	msgs    []string
}

var (
	searchCacheMu sync.Mutex
	searchCache   = map[string]*searchCacheEntry{}
)

func searchHistoryMessages(path string) []string {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	searchCacheMu.Lock()
	defer searchCacheMu.Unlock()
	if c, ok := searchCache[path]; ok && c.modTime == fi.ModTime() && c.size == fi.Size() {
		return c.msgs
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var histRoot struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &histRoot); err != nil {
		return nil
	}
	msgs := make([]string, 0, len(histRoot.Messages))
	for _, m := range histRoot.Messages {
		msgs = append(msgs, m.Content)
	}
	searchCache[path] = &searchCacheEntry{modTime: fi.ModTime(), size: fi.Size(), msgs: msgs}
	return msgs
}

func SearchProjects(query string) ([][]string, []string) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	qLower := strings.ToLower(q)

	projects := ListProjects()
	idToName := make(map[string]string)
	for _, p := range projects {
		idToName[p.ID] = p.Name
	}

	var results [][]string
	var resultIDs []string
	seenPIDs := make(map[string]bool)

	entries, err := os.ReadDir(config.ProjectsDir)
	if err != nil {
		return nil, nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid := entry.Name()
		pname := idToName[pid]
		if pname == "" {
			pname = pid
		}

		// 1. 项目名称匹配
		if strings.Contains(strings.ToLower(pname), qLower) && !seenPIDs[pid] {
			results = append(results, []string{pname, "（项目名称匹配）"})
			resultIDs = append(resultIDs, pid)
			seenPIDs[pid] = true
			continue
		}

		// 2. 对话内容匹配（默认 history.json + 各任务 tasks/*/history.json）
		pdir := filepath.Join(config.ProjectsDir, pid)
		var histFiles []string
		if _, err := os.Stat(filepath.Join(pdir, "history.json")); err == nil {
			histFiles = append(histFiles, filepath.Join(pdir, "history.json"))
		}

		tasksDir := filepath.Join(pdir, "tasks")
		if entries, err := os.ReadDir(tasksDir); err == nil {
			for _, tDir := range entries {
				if tDir.IsDir() {
					th := filepath.Join(tasksDir, tDir.Name(), "history.json")
					if _, err := os.Stat(th); err == nil {
						histFiles = append(histFiles, th)
					}
				}
			}
		}

		for _, hf := range histFiles {
			msgs := searchHistoryMessages(hf)
			if msgs == nil {
				continue
			}

			matched := false
			for _, c := range msgs {
				cLower := strings.ToLower(c)
				if strings.Contains(cLower, qLower) && !seenPIDs[pid] {
					runes := []rune(c)
					lowerRunes := []rune(cLower)
					qRunes := []rune(qLower)

					runeIdx := 0
					for i := 0; i <= len(lowerRunes)-len(qRunes); i++ {
						if string(lowerRunes[i:i+len(qRunes)]) == qLower {
							runeIdx = i
							break
						}
					}

					start := runeIdx - 20
					if start < 0 {
						start = 0
					}
					end := runeIdx + len(qRunes) + 20
					if end > len(runes) {
						end = len(runes)
					}

					snippet := string(runes[start:end])
					if start > 0 {
						snippet = "..." + snippet
					}
					if end < len(runes) {
						snippet = snippet + "..."
					}

					results = append(results, []string{pname, snippet})
					resultIDs = append(resultIDs, pid)
					seenPIDs[pid] = true
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}

	return results, resultIDs
}
