// Package soundlib 维护全局音源注册表（Library/registry.json）。
//
// 注册表回答"这台机器上有哪些音源"：SF2 音色库、采样包目录，以及 P5/P6 的
// VST3 / MM 插件条目。输出机架（项目乐器箱）、轨道头与钢琴窗的音源选择器
// 都从这里取候选，不再各自枚举磁盘或 IndexedDB。
//
// 采样目录存在两个视角：注册表里的采样包条目（含名称/标签/启停等元数据）
// 与历史沿用至今的 settings.MaterialDirs（素材树的实际数据源）。后者作为
// 兼容镜像与前者双向同步——注册表写入时回写 MaterialDirs，启动迁移时把
// MaterialDirs 里新增的目录并入采样包条目。P3 素材库改版后镜像可以退场。
package soundlib

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"melodymate/internal/config"
)

// 音源种类
const (
	KindSF2        = "sf2"
	KindSamplePack = "samplepack"
	KindVST3       = "vst3"
	KindMM         = "mm"
)

// 来源标记
const (
	OriginBuiltin = "builtin"
	OriginUser    = "user"
	OriginAI      = "ai"
)

// 插件用途（vst3 / mm）
const (
	CategoryInstrument = "instrument"
	CategoryEffect     = "effect"
)

// RegistryVersion registry.json 的结构版本
const RegistryVersion = 1

// sf2Exts 视为 SF2 音色库的扩展名。
// 注意：不包含 .sf3 —— 引擎的 tinySoundFont 未编译 Vorbis 解码，把 SF3
// 的压缩采样当 int16 PCM 播放会输出很响的噪声（S-M3）。扫描/导入阶段
// 直接拒绝，而不是"加载成功"后让用户听到噪声。
var sf2Exts = []string{".sf2"}

// AIInfo 标记 AI 生成的音源（设置页/机架/素材库据此显示 AI 徽标）
type AIInfo struct {
	Author    string `json:"author,omitempty"`
	CreatedBy string `json:"createdBy,omitempty"`
}

// Source 注册表条目，字段与《架构升级实施计划》3.2.1 的契约一致。
type Source struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Vendor       string   `json:"vendor,omitempty"`
	Origin       string   `json:"origin"`
	Path         string   `json:"path,omitempty"` // 相对数据根（Library/ 内）或绝对路径
	Dirs         []string `json:"dirs,omitempty"` // 采样包目录
	Category     string   `json:"category,omitempty"`
	ClassUID     string   `json:"classUid,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Hash         string   `json:"hash,omitempty"`
	AI           *AIInfo  `json:"ai,omitempty"`
	Enabled      bool     `json:"enabled"`
	Tags         []string `json:"tags,omitempty"`
	AddedAt      int64    `json:"addedAt"`
	UpdatedAt    int64    `json:"updatedAt"`
}

// Runtime 条目的磁盘实况，每次列举时重算，不落盘。
type Runtime struct {
	AbsPath     string `json:"absPath,omitempty"`
	FileName    string `json:"fileName,omitempty"`
	Missing     bool   `json:"missing,omitempty"`
	Size        int64  `json:"size,omitempty"`
	PresetCount int    `json:"presetCount,omitempty"`
	SampleDirs  int    `json:"sampleDirs,omitempty"`
	SampleFiles int    `json:"sampleFiles,omitempty"`
}

// View 注册表条目 + 磁盘实况（HTTP 响应与前端消费的形状）
type View struct {
	Source
	Runtime Runtime `json:"runtime"`
}

// Registry registry.json 的根结构
type Registry struct {
	Version int      `json:"version"`
	Sources []Source `json:"sources"`
}

// FilePath registry.json 的位置（数据根内，随项目目录一起被迁移/备份）
func FilePath() string {
	return filepath.Join(config.LibraryDir, "registry.json")
}

// SoundFontDir 音色库落盘目录（与 handler_soundfont.go 的上传目标一致）
func SoundFontDir() string {
	return filepath.Join(config.LibraryDir, "soundfonts")
}

// StorePath 把绝对路径转成注册表里保存的形式：数据根内的记相对路径
// （正斜杠，便于整个目录搬迁/换盘符），根外记绝对路径。
func StorePath(abs string) string {
	abs = filepath.Clean(abs)
	root := filepath.Clean(config.ProjectRoot)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return abs
	}
	return filepath.ToSlash(rel)
}

// ResolvePath 把注册表里保存的路径还原为绝对路径
func ResolvePath(stored string) string {
	if stored == "" {
		return ""
	}
	if filepath.IsAbs(stored) {
		return filepath.Clean(stored)
	}
	return filepath.Clean(filepath.Join(config.ProjectRoot, filepath.FromSlash(stored)))
}

// makeID 生成稳定 id：前缀 + 名称摘要 + 路径短哈希。
// 名称摘要便于人工排查，短哈希保证同目录下同名文件（重名不同后缀）
// 与移动后的文件不会互相覆盖。
func makeID(prefix, name, key string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.ToSlash(key))))
	return prefix + "_" + slug(name) + "_" + hex.EncodeToString(sum[:3])
}

// slug 保留字母/数字/连字符/下划线，其余折叠为下划线；长度截到 24
func slug(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' ||
			(r >= 0x4e00 && r <= 0x9fff) // 中文名保留，便于识别
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if len(out) > 24 {
		out = out[:24]
	}
	if out == "" {
		out = "item"
	}
	return out
}

// isSF2Name 判断文件名是否 SF2 音色库
func isSF2Name(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range sf2Exts {
		if ext == e {
			return true
		}
	}
	return false
}

// looksBuiltinSF2 判断是否为安装包随附的 GeneralUser GS。
// 内置音色在设置页显示"内置"，与用户自建条目区分；判定失败只会显示为
// "用户"，不影响功能。
func looksBuiltinSF2(name string) bool {
	n := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	return strings.Contains(n, "generaluser")
}

// runtimeFor 按磁盘实况填充 Runtime（缺失文件不报错，前端据此提示）
func runtimeFor(src *Source) Runtime {
	rt := Runtime{}
	switch src.Kind {
	case KindSF2:
		abs := ResolvePath(src.Path)
		if abs == "" {
			rt.Missing = true
			return rt
		}
		rt.AbsPath = abs
		rt.FileName = filepath.Base(abs)
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			rt.Missing = true
			return rt
		}
		rt.Size = info.Size()
		if meta, err := ReadSF2Info(abs); err == nil {
			rt.PresetCount = meta.PresetCount
			if meta.Name != "" && src.Name == "" {
				src.Name = meta.Name
			}
		}
	case KindSamplePack:
		files, dirs := 0, 0
		for _, d := range src.Dirs {
			abs := ResolvePath(d)
			if abs == "" {
				continue
			}
			n, sub := countAudioFiles(abs)
			if n < 0 {
				continue // 目录不可访问（未插盘等）：不计入，也不判缺失
			}
			files += n
			dirs += sub
		}
		rt.SampleFiles = files
		rt.SampleDirs = dirs
		rt.Missing = len(src.Dirs) > 0 && files == 0 && dirs == 0
	}
	return rt
}

// countAudioFiles 统计目录下的音频文件与其子目录数（仅一层，
// 与素材树的懒加载语义一致）。目录不可访问返回 (-1, -1)。
func countAudioFiles(dir string) (int, int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return -1, -1
	}
	files, dirs := 0, 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			dirs++
			continue
		}
		if audioExts[strings.ToLower(filepath.Ext(name))] {
			files++
		}
	}
	return files, dirs
}

// audioExts 采样/素材可识别的音频扩展名（与 handler_arrangement.go 的
// arrangeAudioExt 保持一致，后者是 HTTP 白名单，这里是磁盘统计）
var audioExts = map[string]bool{
	".wav": true, ".wave": true, ".mp3": true, ".ogg": true, ".oga": true,
	".flac": true, ".m4a": true, ".aac": true,
}

// IsAudioPath 判断路径是否为可识别的音频文件（按扩展名，不看磁盘）
func IsAudioPath(p string) bool {
	return audioExts[strings.ToLower(filepath.Ext(strings.TrimSpace(p)))]
}

// SortSources 稳定排序：种类 → 名称（前端分组展示依赖它保持顺序稳定）
func SortSources(list []Source) {
	kindRank := func(k string) int {
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
	sort.SliceStable(list, func(i, j int) bool {
		if kindRank(list[i].Kind) != kindRank(list[j].Kind) {
			return kindRank(list[i].Kind) < kindRank(list[j].Kind)
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
}
