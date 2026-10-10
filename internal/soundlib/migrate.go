package soundlib

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"melodymate/internal/config"
)

// DefaultPackName 兼容镜像的采样包条目名：settings.MaterialDirs 在注册表里
// 的落点。此条目由启动迁移维护（按 origin 识别，不随用户改名消失）。
const DefaultPackName = "默认素材"

// MigrateResult 启动迁移结果
type MigrateResult struct {
	SoundFonts int  `json:"soundFonts"` // 新入册的 SF2 音色库
	Packs      int  `json:"packs"`      // 新入册的采样包
	Dirs       int  `json:"dirs"`       // 新并入采样包的目录
	Changed    bool `json:"changed"`
}

// Migrate 把磁盘上已存在但尚未入册的音源补进注册表，并同步采样目录镜像。
// 幂等：每次启动都跑，没有新东西就什么都不写。
//
// 覆盖两类老用户数据：
//   - Library/soundfonts/ 下已有的 SF2（历史版本只落盘、无清单）
//   - settings.MaterialDirs 里已注册的素材目录（历史版本只存设置、无音源视图）
func Migrate() (MigrateResult, error) {
	mu.Lock()
	defer mu.Unlock()

	var res MigrateResult
	reg := loadLocked()
	fonts, packs, dirs := absorbDiskLocked(reg)
	res.SoundFonts, res.Packs, res.Dirs = fonts, packs, dirs

	registryDirty := res.SoundFonts > 0 || res.Packs > 0 || res.Dirs > 0
	settingsDirty := syncMaterialDirsLocked(reg)
	res.Changed = registryDirty || settingsDirty

	if registryDirty {
		if err := saveLocked(reg); err != nil {
			return res, fmt.Errorf("保存音源注册表失败: %w", err)
		}
		slog.Info("[soundlib] 音源注册表已更新",
			"soundfonts", res.SoundFonts, "packs", res.Packs, "dirs", res.Dirs)
	}
	return res, nil
}

// absorbDiskLocked 扫描磁盘并补齐注册表条目，返回新入册的
// （SF2 数, 采样包数, 目录数）。调用方需持有 mu。
func absorbDiskLocked(reg *Registry) (int, int, int) {
	addedPacks := 0
	knownSF2 := map[string]bool{}
	ids := map[string]bool{}
	for i := range reg.Sources {
		ids[reg.Sources[i].ID] = true
		if reg.Sources[i].Kind == KindSF2 {
			knownSF2[strings.ToLower(ResolvePath(reg.Sources[i].Path))] = true
		}
	}

	// 1. 扫描音色库目录
	files, err := os.ReadDir(SoundFontDir())
	if err != nil && !os.IsNotExist(err) {
		slog.Warn("[soundlib] 读取音色库目录失败", "dir", SoundFontDir(), "err", err)
	}
	addedFonts := 0
	for _, e := range files {
		if e.IsDir() || !isSF2Name(e.Name()) {
			continue
		}
		abs := filepath.Join(SoundFontDir(), e.Name())
		if knownSF2[strings.ToLower(abs)] {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		src := Source{
			ID:      makeID("sf2", base, abs),
			Kind:    KindSF2,
			Name:    base,
			Origin:  OriginUser,
			Path:    StorePath(abs),
			Enabled: true,
		}
		if looksBuiltinSF2(e.Name()) {
			src.Origin = OriginBuiltin
		}
		if ids[src.ID] {
			continue
		}
		stamp(&src)
		reg.Sources = append(reg.Sources, src)
		ids[src.ID] = true
		knownSF2[strings.ToLower(abs)] = true
		addedFonts++
	}

	// 2. 采样目录：settings.MaterialDirs → 注册表采样包条目
	managed := map[string]bool{}
	for _, s := range reg.Sources {
		if s.Kind == KindSamplePack && s.Origin != OriginBuiltin {
			for _, d := range s.Dirs {
				managed[strings.ToLower(filepath.Clean(d))] = true
			}
		}
	}
	unmanaged := func(dirs []string) []string {
		var out []string
		for _, d := range dirs {
			if !managed[strings.ToLower(filepath.Clean(d))] {
				out = append(out, d)
			}
		}
		return out
	}
	legacy := unmanaged(config.LoadSettings().MaterialDirs)
	idx := -1
	for i := range reg.Sources {
		if reg.Sources[i].Kind == KindSamplePack && reg.Sources[i].Origin == OriginBuiltin {
			idx = i
			break
		}
	}
	if idx < 0 && len(legacy) > 0 {
		src := Source{
			ID:      makeID("pack", DefaultPackName, "material_dirs"),
			Kind:    KindSamplePack,
			Name:    DefaultPackName,
			Origin:  OriginBuiltin,
			Enabled: true,
		}
		stamp(&src)
		if !ids[src.ID] {
			reg.Sources = append(reg.Sources, src)
			idx = len(reg.Sources) - 1
			addedPacks++
		}
	}
	addedDirs := 0
	if idx >= 0 {
		before := len(reg.Sources[idx].Dirs)
		merged := normalizeDirs(unmanaged(append(append([]string{}, reg.Sources[idx].Dirs...), legacy...)))
		if !sameDirList(merged, reg.Sources[idx].Dirs) {
			reg.Sources[idx].Dirs = merged
			reg.Sources[idx].UpdatedAt = time.Now().Unix()
			addedDirs = len(merged) - before
			if addedDirs <= 0 {
				addedDirs = 1
			}
		}
	}
	return addedFonts, addedPacks, addedDirs
}

// stamp 为新条目补时间戳
func stamp(src *Source) {
	normalizeSource(src)
	now := time.Now().Unix()
	src.AddedAt, src.UpdatedAt = now, now
}

// SyncMaterialDirs 注册表变更后同步素材目录镜像（供 HTTP handler 调用）
func SyncMaterialDirs() {
	mu.Lock()
	defer mu.Unlock()
	reg := loadLocked()
	if syncMaterialDirsLocked(reg) {
		if err := saveLocked(reg); err != nil {
			slog.Warn("[soundlib] 素材目录镜像保存失败", "err", err)
		}
	}
}

// syncMaterialDirsLocked 把启用中的采样包目录写回 settings.MaterialDirs。
// 素材树（/api/arrangement/dirs）仍读该字段，P3 素材库改版后才由注册表直供。
// 返回是否发生变化。调用方需持有 mu。
func syncMaterialDirsLocked(reg *Registry) bool {
	next := flattenPackDirs(reg)
	cur := config.LoadSettings().MaterialDirs
	if sameDirList(cur, next) {
		return false
	}
	s := config.LoadSettings()
	s.MaterialDirs = next
	if err := config.SaveSettings(s); err != nil {
		slog.Warn("[soundlib] 素材目录镜像写回失败（注册表已保存）", "err", err)
		return false
	}
	return true
}

// flattenPackDirs 汇总启用中的采样包目录
func flattenPackDirs(reg *Registry) []string {
	var all []string
	for i := range reg.Sources {
		if reg.Sources[i].Kind != KindSamplePack || !reg.Sources[i].Enabled {
			continue
		}
		all = append(all, reg.Sources[i].Dirs...)
	}
	out := normalizeDirs(all)
	if out == nil {
		out = []string{}
	}
	return out
}

// sameDirList 忽略顺序与大小写比较两个目录列表
func sameDirList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, d := range a {
		seen[strings.ToLower(filepath.Clean(d))]++
	}
	for _, d := range b {
		key := strings.ToLower(filepath.Clean(d))
		seen[key]--
		if seen[key] < 0 {
			return false
		}
	}
	return true
}
