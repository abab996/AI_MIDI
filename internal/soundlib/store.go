package soundlib

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"melodymate/internal/config"
)

// ErrNotFound 条目不存在
var ErrNotFound = errors.New("音源条目不存在")

// mu 串行化注册表的读-改-写。HTTP handler、启动迁移与导入都走这里，
// 没有它两个并发请求会各自读到旧快照、后写覆盖先写。
var mu sync.Mutex

// Load 读取注册表；文件缺失或损坏时返回空注册表而非错误——
// 注册表是缓存性质的元数据，可随时由磁盘重新生成，不该让应用起不来。
func Load() *Registry {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked()
}

func loadLocked() *Registry {
	reg := &Registry{Version: RegistryVersion, Sources: []Source{}}
	raw, err := os.ReadFile(FilePath())
	if err != nil {
		return reg
	}
	var parsed Registry
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return reg
	}
	if parsed.Version <= 0 {
		parsed.Version = RegistryVersion
	}
	if parsed.Sources == nil {
		parsed.Sources = []Source{}
	}
	return &parsed
}

// saveLocked 原子落盘：唯一临时名 + rename。
// 固定 ".tmp" 名会让两个并发写互相踩踏（一个 rename 后另一个的 fd 已失效）。
func saveLocked(reg *Registry) error {
	if original, err := os.ReadFile(FilePath()); err == nil {
		if !json.Valid(original) {
			return fmt.Errorf("注册表损坏，拒绝覆盖原始文件")
		}
		backup := FilePath() + ".before-audit-2026-10-01"
		f, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(original)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		} else if !os.IsExist(err) {
			return err
		}
	}

	if reg.Version <= 0 {
		reg.Version = RegistryVersion
	}
	SortSources(reg.Sources)
	raw, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.LibraryDir, 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", FilePath(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, FilePath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// List 按种类与关键词过滤，返回带磁盘实况的条目（关键词匹配名称/厂商/标签/路径）
func List(kind, query string) []View {
	mu.Lock()
	reg := loadLocked()
	mu.Unlock()

	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]View, 0, len(reg.Sources))
	for i := range reg.Sources {
		src := reg.Sources[i]
		if kind != "" && src.Kind != kind {
			continue
		}
		if q != "" && !matches(src, q) {
			continue
		}
		rt := runtimeFor(&src)
		out = append(out, View{Source: src, Runtime: rt})
	}
	SortViews(out)
	return out
}

// matches 关键词命中判定：名称/厂商/标签/文件名/预设名之外的路径
func matches(src Source, q string) bool {
	if strings.Contains(strings.ToLower(src.Name), q) ||
		strings.Contains(strings.ToLower(src.Vendor), q) ||
		strings.Contains(strings.ToLower(src.Path), q) {
		return true
	}
	for _, t := range src.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	for _, d := range src.Dirs {
		if strings.Contains(strings.ToLower(d), q) {
			return true
		}
	}
	return false
}

// SortViews 与 SortSources 同序，作用于带实况的视图
func SortViews(list []View) {
	srcs := make([]Source, len(list))
	for i := range list {
		srcs[i] = list[i].Source
	}
	idx := make([]int, len(list))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return lessSource(srcs[idx[a]], srcs[idx[b]])
	})
	out := make([]View, len(list))
	for i, j := range idx {
		out[i] = list[j]
	}
	copy(list, out)
}

func lessSource(a, b Source) bool {
	ra, rb := kindRank(a.Kind), kindRank(b.Kind)
	if ra != rb {
		return ra < rb
	}
	return strings.ToLower(a.Name) < strings.ToLower(b.Name)
}

func kindRank(k string) int {
	switch k {
	case KindSF2:
		return 0
	case KindSamplePack:
		return 1
	case KindVST3:
		return 2
	case KindMM:
		return 3
	}
	return 9
}

// Get 按 id 取单条
func Get(id string) (View, error) {
	mu.Lock()
	reg := loadLocked()
	mu.Unlock()
	for i := range reg.Sources {
		if reg.Sources[i].ID == id {
			src := reg.Sources[i]
			return View{Source: src, Runtime: runtimeFor(&src)}, nil
		}
	}
	return View{}, ErrNotFound
}

// Add 新增条目。id 为空时按 kind+名称+路径生成；已存在同 id 则报错
// （调用方应改用 Update），避免静默覆盖用户改名过的条目。
func Add(src Source) (Source, error) {
	mu.Lock()
	defer mu.Unlock()
	reg := loadLocked()
	normalizeSource(&src)
	if src.ID == "" {
		src.ID = makeID(idPrefix(src.Kind), src.Name, sourceKeyOf(src))
	}
	for i := range reg.Sources {
		if reg.Sources[i].ID == src.ID {
			return Source{}, fmt.Errorf("音源条目已存在: %s", src.ID)
		}
	}
	now := time.Now().Unix()
	if src.AddedAt == 0 {
		src.AddedAt = now
	}
	src.UpdatedAt = now
	reg.Sources = append(reg.Sources, src)
	if err := saveLocked(reg); err != nil {
		return Source{}, err
	}
	return src, nil
}

// Update 对条目做读-改-写；mutate 返回错误则整体放弃（不落盘）
func Update(id string, mutate func(*Source) error) (Source, error) {
	mu.Lock()
	defer mu.Unlock()
	reg := loadLocked()
	for i := range reg.Sources {
		if reg.Sources[i].ID != id {
			continue
		}
		next := reg.Sources[i]
		if err := mutate(&next); err != nil {
			return Source{}, err
		}
		next.ID = id // id 不可改：前端持久化的引用靠它
		normalizeSource(&next)
		if reflect.DeepEqual(next, reg.Sources[i]) {
			return next, nil
		}
		next.UpdatedAt = time.Now().Unix()
		reg.Sources[i] = next
		if err := saveLocked(reg); err != nil {
			return Source{}, err
		}
		return next, nil
	}
	return Source{}, ErrNotFound
}

// Delete 删除条目，返回被删的条目（供调用方决定是否连带删文件）
func Delete(id string) (Source, error) {
	mu.Lock()
	defer mu.Unlock()
	reg := loadLocked()
	for i := range reg.Sources {
		if reg.Sources[i].ID != id {
			continue
		}
		removed := reg.Sources[i]
		reg.Sources = append(reg.Sources[:i], reg.Sources[i+1:]...)
		if err := saveLocked(reg); err != nil {
			return Source{}, err
		}
		return removed, nil
	}
	return Source{}, ErrNotFound
}

// normalizeSource 收敛外部输入：补默认值、去重、规范化路径
func normalizeSource(src *Source) {
	src.Name = strings.TrimSpace(src.Name)
	src.Vendor = strings.TrimSpace(src.Vendor)
	src.Kind = strings.ToLower(strings.TrimSpace(src.Kind))
	if src.Origin == "" {
		src.Origin = OriginUser
	}
	switch src.Kind {
	case KindSF2:
		if src.Path != "" {
			src.Path = StorePath(ResolvePath(src.Path))
		}
		src.Dirs = nil
	case KindSamplePack:
		src.Dirs = normalizeDirs(src.Dirs)
		src.Path = ""
	case KindVST3, KindMM:
		if src.Path != "" {
			src.Path = StorePath(ResolvePath(src.Path))
		}
		if src.Category == "" {
			src.Category = CategoryInstrument
		}
		src.Dirs = nil
	default:
		src.Dirs = normalizeDirs(src.Dirs)
	}
	if src.Tags != nil {
		tags := make([]string, 0, len(src.Tags))
		seen := map[string]bool{}
		for _, t := range src.Tags {
			t = strings.TrimSpace(t)
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			tags = append(tags, t)
		}
		src.Tags = tags
	}
}

// normalizeDirs 去重 + 规范化采样包目录（大小写不敏感的 Windows 语义）
func normalizeDirs(dirs []string) []string {
	out := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, d := range dirs {
		d = filepath.Clean(strings.TrimSpace(d))
		if d == "" || d == "." {
			continue
		}
		key := strings.ToLower(d)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
}

func idPrefix(kind string) string {
	switch kind {
	case KindSF2:
		return "sf2"
	case KindSamplePack:
		return "pack"
	case KindVST3:
		return "vst3"
	case KindMM:
		return "mm"
	}
	return "src"
}

// sourceKeyOf 生成 id 用的稳定键：SF2/插件用文件路径，采样包用首个目录
func sourceKeyOf(src Source) string {
	if src.Path != "" {
		return src.Path
	}
	if len(src.Dirs) > 0 {
		return src.Dirs[0]
	}
	return src.Name
}

// EnsureSF2Source 为已落盘的 SF2 文件登记条目（文件路径即身份）。
// 已存在同路径条目时原样返回（created=false），不覆盖用户改过的名称/标签。
// displayName 为空时用文件名（去扩展名）——落盘名经过消毒（空格→下划线），
// 传入用户在界面上看到的原名可让条目名更好认。
func EnsureSF2Source(absPath, displayName string) (Source, bool, error) {
	absPath = filepath.Clean(absPath)
	mu.Lock()
	reg := loadLocked()
	for i := range reg.Sources {
		if reg.Sources[i].Kind == KindSF2 &&
			strings.EqualFold(ResolvePath(reg.Sources[i].Path), absPath) {
			src := reg.Sources[i]
			mu.Unlock()
			return src, false, nil
		}
	}
	mu.Unlock()

	base := strings.TrimSpace(displayName)
	if base == "" {
		base = strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	}
	src := Source{
		ID:      makeID("sf2", base, absPath),
		Kind:    KindSF2,
		Name:    base,
		Origin:  OriginUser,
		Path:    StorePath(absPath),
		Enabled: true,
	}
	if looksBuiltinSF2(filepath.Base(absPath)) {
		src.Origin = OriginBuiltin
	}
	out, err := Add(src)
	if err != nil {
		return Source{}, false, err
	}
	return out, true, nil
}

// RemoveSF2ByPath 按文件路径移除注册表条目（旧 /api/audio/soundfonts 删除
// 通道用）。找不到对应条目不报错——历史数据里本来就可能没登记过。
func RemoveSF2ByPath(absPath string) (bool, error) {
	absPath = filepath.Clean(absPath)
	mu.Lock()
	reg := loadLocked()
	for i := range reg.Sources {
		if reg.Sources[i].Kind != KindSF2 ||
			!strings.EqualFold(ResolvePath(reg.Sources[i].Path), absPath) {
			continue
		}
		reg.Sources = append(reg.Sources[:i], reg.Sources[i+1:]...)
		if err := saveLocked(reg); err != nil {
			mu.Unlock()
			return false, err
		}
		mu.Unlock()
		return true, nil
	}
	mu.Unlock()
	return false, nil
}

// DirsForPack 返回启用中的采样包目录（素材树按此列出；含 legacy 参数
// settings.MaterialDirs 中尚未入册的目录，保证迁移期不丢目录）
func DirsForPack() []string {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" {
			return
		}
		key := strings.ToLower(d)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, d)
	}
	for _, v := range List(KindSamplePack, "") {
		if !v.Enabled {
			continue
		}
		for _, d := range v.Dirs {
			add(ResolvePath(d))
		}
	}
	for _, d := range config.LoadSettings().MaterialDirs {
		add(filepath.Clean(d))
	}
	return out
}
