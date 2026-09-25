package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------- 数据模型 ----------------

type GroupStat struct {
	Name    string
	Color   string
	Files   int
	Code    int
	Comment int
	Blank   int
	Total   int
	Size    int64
}

type FileStat struct {
	Rel   string
	Code  int
	Total int
}

type FileDetail struct {
	Rel     string
	Group   string
	Color   string
	Code    int
	Comment int
	Blank   int
	Total   int
	Size    int64
}

type Contrib struct {
	Name  string
	Count int
}

type Stats struct {
	Root         string
	HasGit       bool
	Branch       string
	Hash         string
	Tag          string
	LastAuthor   string
	LastMsg      string
	LastTime     time.Time
	FirstTime    time.Time
	TotalCommits int
	C7, C30      int
	Dirty        int
	NContrib     int
	Contributors []Contrib
	Buckets      [26]int
	WeekMax      int

	Mods           []*GroupStat
	Langs          []*GroupStat
	Totals         GroupStat
	Top            []FileStat
	GroupTop       map[string][]FileStat
	FileMap        map[string]FileDetail
	WorkSize       int64
	GitSize        int64
	MaxFileSize    int64
	MaxFileSizeRel string
	GoVer          string
	GoDeps         int
	FilesFound     int
	DirsFound      int
	Elapsed        time.Duration
}

// ---------------- 扫描配置 ----------------

var excludeDirs = map[string]bool{
	".git": true, ".github": true, ".trae": true, ".workbuddy": true, ".wrangler": true,
	".zcode": true, ".idea": true, ".vscode": true, "node_modules": true, "build": true,
	"dist": true, "bin": true, "out": true, "obj": true, "ThirdParty": true, "JUCE": true,
	"JuceLibraryCode": true, "Library": true, "output": true, "projects": true,
	"__pycache__": true, "原版Python后端": true, "winres": true, "wailsjs": true,
	"vendor": true, "demo": true, "promo": true, "external": true, "DerivedData": true,
}

type langDef struct{ name, style string }

var langMap = map[string]langDef{
	".go": {"Go", "c"}, ".js": {"JavaScript", "c"}, ".mjs": {"JavaScript", "c"},
	".cjs": {"JavaScript", "c"}, ".jsx": {"JavaScript", "c"},
	".ts": {"TypeScript", "c"}, ".tsx": {"TypeScript", "c"},
	".c": {"C/C++", "c"}, ".cpp": {"C/C++", "c"}, ".cc": {"C/C++", "c"},
	".h": {"C/C++", "c"}, ".hpp": {"C/C++", "c"},
	".css": {"CSS", "c"}, ".scss": {"CSS", "c"}, ".less": {"CSS", "c"},
	".html": {"HTML", "html"}, ".htm": {"HTML", "html"}, ".xml": {"XML", "html"},
	".json": {"JSON", "none"},
	".yml":  {"YAML/TOML", "hash"}, ".yaml": {"YAML/TOML", "hash"}, ".toml": {"YAML/TOML", "hash"},
	".py": {"Python", "hash"}, ".md": {"Markdown", "none"},
	".sh": {"脚本", "hash"}, ".bat": {"脚本", "bat"}, ".cmd": {"脚本", "bat"}, ".ps1": {"脚本", "hash"},
	".iss": {"Inno/配置", "semi"}, ".isl": {"Inno/配置", "semi"},
}

type modDef struct {
	key   string
	color string
	match func(rel string) bool
}

var sep = string(os.PathSeparator)

var modDefs = []modDef{
	{"Go 后端", "#56B6C2", func(rel string) bool {
		return strings.HasPrefix(rel, "internal"+sep) || rel == "main.go" || rel == "go.mod" || rel == "go.sum"
	}},
	{"Web 前端", "#98C379", func(rel string) bool { return strings.HasPrefix(rel, "frontend"+sep) }},
	{"C++ 音频引擎", "#C678DD", func(rel string) bool { return strings.HasPrefix(rel, "engine"+sep) }},
	{"测试", "#E5C07B", func(rel string) bool { return strings.HasPrefix(rel, "tests"+sep) }},
	{"工具与脚本", "#61AFEF", func(rel string) bool {
		if strings.HasPrefix(rel, "tools"+sep) {
			return true
		}
		if !strings.ContainsRune(rel, os.PathSeparator) {
			switch strings.ToLower(filepath.Ext(rel)) {
			case ".bat", ".cmd", ".ps1", ".sh":
				return true
			}
		}
		return false
	}},
	{"文档与资源", "#ABB2BF", func(rel string) bool {
		base := filepath.Base(rel)
		return strings.HasPrefix(rel, "docs"+sep) || strings.HasPrefix(base, "README") ||
			base == "LICENSE" || strings.HasPrefix(rel, "Languages"+sep) ||
			strings.EqualFold(filepath.Ext(rel), ".iss") || strings.EqualFold(filepath.Ext(rel), ".isl")
	}},
}

var otherColor = "#5C6370"

// 语言配色（按排序索引取色）
var palette = []string{
	"#56B6C2", "#E5C07B", "#61AFEF", "#C678DD", "#D19A66", "#E06C75",
	"#98C379", "#B5BD68", "#8BE9FD", "#FF79C6", "#ABB2BF", "#5C6370",
}

// ---------------- 行分类 ----------------

func classify(data []byte, style string) (total, code, comment, blank int) {
	inBlock := false
	for _, raw := range strings.Split(string(data), "\n") {
		total++
		t := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if t == "" {
			blank++
			continue
		}
		if inBlock {
			comment++
			if (style == "c" && strings.Contains(t, "*/")) ||
				(style == "html" && strings.Contains(t, "-->")) {
				inBlock = false
			}
			continue
		}
		switch style {
		case "c":
			if strings.HasPrefix(t, "//") {
				comment++
			} else if strings.Contains(t, "/*") {
				comment++
				if !strings.Contains(t, "*/") {
					inBlock = true
				}
			} else {
				code++
			}
		case "html":
			if strings.HasPrefix(t, "<!--") {
				comment++
				if !strings.Contains(t, "-->") {
					inBlock = true
				}
			} else {
				code++
			}
		case "hash":
			if strings.HasPrefix(t, "#") {
				comment++
			} else {
				code++
			}
		case "semi":
			if strings.HasPrefix(t, ";") {
				comment++
			} else {
				code++
			}
		case "bat":
			low := strings.ToLower(t)
			if low == "rem" || strings.HasPrefix(low, "rem ") || strings.HasPrefix(t, "::") {
				comment++
			} else {
				code++
			}
		default:
			code++
		}
	}
	return
}

// ---------------- 文件遍历 ----------------

type fileEntry struct {
	path string
	size int64
}

func walkFiles(root string) ([]fileEntry, int) {
	var files []fileEntry
	dirs := 0
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				dirs++
				if !excludeDirs[e.Name()] {
					stack = append(stack, p)
				}
			} else if e.Type().IsRegular() {
				var sz int64
				if fi, err := e.Info(); err == nil {
					sz = fi.Size()
				}
				files = append(files, fileEntry{p, sz})
			}
		}
	}
	return files, dirs
}

func dirSize(root string) int64 {
	var sum int64
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				stack = append(stack, p)
			} else if e.Type().IsRegular() {
				if fi, err := e.Info(); err == nil {
					sum += fi.Size()
				}
			}
		}
	}
	return sum
}

// ---------------- Git ----------------

func hasGit() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func gitOut(root string, args ...string) ([]string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, true
}

func gitOne(root string, args ...string) (string, bool) {
	lines, ok := gitOut(root, args...)
	if !ok || len(lines) == 0 {
		return "", false
	}
	return lines[0], true
}

func parseGit(s *Stats) {
	if b, ok := gitOne(s.Root, "rev-parse", "--abbrev-ref", "HEAD"); ok {
		s.Branch = b
	}
	if h, ok := gitOne(s.Root, "rev-parse", "--short", "HEAD"); ok {
		s.Hash = h
	}
	if t, ok := gitOne(s.Root, "describe", "--tags", "--abbrev=0"); ok {
		s.Tag = t
	}
	if c, ok := gitOne(s.Root, "rev-list", "--count", "HEAD"); ok {
		s.TotalCommits, _ = strconv.Atoi(c)
	}
	if c, ok := gitOne(s.Root, "rev-list", "--count", "--since=7 days ago", "HEAD"); ok {
		s.C7, _ = strconv.Atoi(c)
	}
	if c, ok := gitOne(s.Root, "rev-list", "--count", "--since=30 days ago", "HEAD"); ok {
		s.C30, _ = strconv.Atoi(c)
	}
	if line, ok := gitOne(s.Root, "log", "-1", "--format=%h|%at|%an|%s"); ok {
		p := strings.SplitN(line, "|", 4)
		if len(p) >= 4 {
			s.Hash = p[0]
			if sec, err := strconv.ParseInt(p[1], 10, 64); err == nil {
				s.LastTime = time.Unix(sec, 0)
			}
			s.LastAuthor = p[2]
			s.LastMsg = p[3]
		}
	}
	if lines, ok := gitOut(s.Root, "log", "--reverse", "--format=%at"); ok && len(lines) > 0 {
		if sec, err := strconv.ParseInt(lines[0], 10, 64); err == nil {
			s.FirstTime = time.Unix(sec, 0)
		}
	}
	if lines, ok := gitOut(s.Root, "status", "--porcelain"); ok {
		s.Dirty = len(lines)
	}
	if lines, ok := gitOut(s.Root, "log", "--format=%an"); ok {
		counts := map[string]int{}
		for _, name := range lines {
			counts[name]++
		}
		s.NContrib = len(counts)
		for name, c := range counts {
			s.Contributors = append(s.Contributors, Contrib{name, c})
		}
		sort.Slice(s.Contributors, func(i, j int) bool {
			if s.Contributors[i].Count != s.Contributors[j].Count {
				return s.Contributors[i].Count > s.Contributors[j].Count
			}
			return s.Contributors[i].Name < s.Contributors[j].Name
		})
		if len(s.Contributors) > 6 {
			s.Contributors = s.Contributors[:6]
		}
	}
	if lines, ok := gitOut(s.Root, "log", "--since=26 weeks ago", "--format=%at"); ok {
		now := time.Now()
		for _, l := range lines {
			sec, err := strconv.ParseInt(l, 10, 64)
			if err != nil {
				continue
			}
			age := int(now.Sub(time.Unix(sec, 0)).Hours() / 24)
			if age < 0 {
				age = 0
			}
			b := 25 - age/7
			if b >= 0 && b <= 25 {
				s.Buckets[b]++
			}
		}
		for _, v := range s.Buckets {
			if v > s.WeekMax {
				s.WeekMax = v
			}
		}
	}
}

// ---------------- 主扫描 ----------------

func Scan(root string, progress chan<- int) (*Stats, error) {
	start := time.Now()
	s := &Stats{Root: root, GroupTop: map[string][]FileStat{}, FileMap: map[string]FileDetail{}}

	files, dirs := walkFiles(root)
	s.FilesFound, s.DirsFound = len(files), dirs
	s.HasGit = hasGit()
	if s.HasGit {
		parseGit(s)
	}

	langAgg := map[string]*GroupStat{}
	modAgg := map[string]*GroupStat{}
	var modOther *GroupStat

	n := 0
	for _, fe := range files {
		n++
		if progress != nil && n%25 == 0 {
			progress <- n
		}
		ext := strings.ToLower(filepath.Ext(fe.path))
		ld, ok := langMap[ext]
		if !ok {
			continue
		}
		rel, err := filepath.Rel(root, fe.path)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(fe.path)
		if err != nil {
			continue
		}
		total, code, comment, blank := classify(data, ld.style)

		if lg, exists := langAgg[ld.name]; exists {
			lg.Files, lg.Code, lg.Comment, lg.Blank, lg.Total, lg.Size =
				lg.Files+1, lg.Code+code, lg.Comment+comment, lg.Blank+blank, lg.Total+total, lg.Size+fe.size
		} else {
			langAgg[ld.name] = &GroupStat{Name: ld.name, Files: 1, Code: code, Comment: comment, Blank: blank, Total: total, Size: fe.size}
		}

		modName := ""
		color := ""
		for i := range modDefs {
			if modDefs[i].match(rel) {
				modName, color = modDefs[i].key, modDefs[i].color
				break
			}
		}
		if modName != "" {
			if mg, exists := modAgg[modName]; exists {
				mg.Files, mg.Code, mg.Comment, mg.Blank, mg.Total, mg.Size =
					mg.Files+1, mg.Code+code, mg.Comment+comment, mg.Blank+blank, mg.Total+total, mg.Size+fe.size
			} else {
				modAgg[modName] = &GroupStat{Name: modName, Color: color, Files: 1, Code: code, Comment: comment, Blank: blank, Total: total, Size: fe.size}
			}
		} else {
			if modOther == nil {
				modOther = &GroupStat{Name: "其他/未归类", Color: otherColor}
			}
			modOther.Files, modOther.Code, modOther.Comment, modOther.Blank, modOther.Total, modOther.Size =
				modOther.Files+1, modOther.Code+code, modOther.Comment+comment, modOther.Blank+blank, modOther.Total+total, modOther.Size+fe.size
		}

		detail := FileDetail{Rel: rel, Code: code, Comment: comment, Blank: blank, Total: total, Size: fe.size, Group: modName, Color: color}
		if detail.Color == "" {
			detail.Color = otherColor
		}
		s.FileMap[rel] = detail
		if fe.size > s.MaxFileSize {
			s.MaxFileSize = fe.size
			s.MaxFileSizeRel = rel
		}

		if code > 0 {
			fs := FileStat{rel, code, total}
			s.Top = insertTop(s.Top, fs, 10)
			if modName != "" {
				s.GroupTop[modName] = insertTop(s.GroupTop[modName], fs, 3)
			}
			s.GroupTop[ld.name] = insertTop(s.GroupTop[ld.name], fs, 3)
		}
	}
	if progress != nil {
		progress <- n
	}

	// 模块序列（按定义顺序，仅保留有内容的）
	for i := range modDefs {
		if g, exists := modAgg[modDefs[i].key]; exists {
			s.Mods = append(s.Mods, g)
		}
	}
	if modOther != nil && modOther.Files > 0 {
		s.Mods = append(s.Mods, modOther)
	}
	// 语言序列（按代码行排序）
	for _, g := range langAgg {
		s.Langs = append(s.Langs, g)
	}
	sort.Slice(s.Langs, func(i, j int) bool { return s.Langs[i].Code > s.Langs[j].Code })
	for i, g := range s.Langs {
		g.Color = palette[i%len(palette)]
	}

	// 汇总
	for _, g := range s.Mods {
		s.Totals.Files += g.Files
		s.Totals.Code += g.Code
		s.Totals.Comment += g.Comment
		s.Totals.Blank += g.Blank
		s.Totals.Total += g.Total
		s.Totals.Size += g.Size
	}

	// 语言配色需要同步到 FileMap（语言色与模块色不同时以模块色为准）
	s.WorkSize = 0
	for _, fe := range files {
		s.WorkSize += fe.size
	}
	if s.HasGit {
		s.GitSize = dirSize(filepath.Join(root, ".git"))
	}
	if gm, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		if m := regexp.MustCompile(`(?m)^go\s+([\d.]+)`).FindSubmatch(gm); m != nil {
			s.GoVer = string(m[1])
		}
		s.GoDeps = len(regexp.MustCompile(`(?m)^\s*[\w./\-]+\s+v[\w.\-+]`).FindAll(gm, -1))
	}
	s.Elapsed = time.Since(start)
	return s, nil
}

func insertTop(list []FileStat, fs FileStat, cap int) []FileStat {
	list = append(list, fs)
	sort.Slice(list, func(i, j int) bool { return list[i].Code > list[j].Code })
	if len(list) > cap {
		list = list[:cap]
	}
	return list
}

func SizeStr(b int64) string {
	const kb, mb, gb = 1 << 10, 1 << 20, 1 << 30
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB", float64(b)/gb)
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/mb)
	default:
		return fmt.Sprintf("%.1f KB", float64(b)/kb)
	}
}
