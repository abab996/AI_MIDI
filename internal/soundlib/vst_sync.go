package soundlib

import (
	"melodymate/internal/engine"
	"path/filepath"
	"strings"
)

// SyncVST3Plugins registers scanned instruments as rack sources as well as FX.
// Refresh enriches old identifiers and retains the user's enabled state and name.
//
// 匹配以 ClassUID（引擎的 format|路径|uniqueId）优先：插件移动/重装后
// 路径变了，但 uniqueId 不变，旧注册表条目会被就地更新而不是新增一条
// 幽灵条目（P-M1）。路径匹配只作为 ClassUID 缺失时的兜底。
func SyncVST3Plugins(scanned []engine.PluginInfo) error {
	pathKey := func(path string) string { return strings.ToLower(filepath.Clean(ResolvePath(path))) }
	counts := map[string]int{}
	for _, p := range scanned {
		counts[pathKey(p.Path)]++
	}
	/* 旧注册表快照取一次循环外用：List 内部有磁盘 IO，放循环里是 O(n²) */
	existing := List(KindVST3, "")
	for _, p := range scanned {
		matched := ""
		for _, old := range existing {
			if old.ClassUID != "" && old.ClassUID == p.ID {
				matched = old.ID
				break
			}
		}
		if matched == "" {
			for _, old := range existing {
				if pathKey(old.Path) == pathKey(p.Path) && (old.ClassUID == p.ID || old.ClassUID != "" && strings.HasSuffix(p.ID, "|"+old.ClassUID) || old.ClassUID == "" && counts[pathKey(p.Path)] == 1) {
					matched = old.ID
					break
				}
			}
		}
		if matched != "" {
			cur, gerr := Get(matched)
			if gerr == nil && cur.ClassUID == p.ID && cur.Category == p.Category && cur.Path == StorePath(p.Path) && (cur.Vendor != "" || p.Vendor == "") {
				continue
			}
			if _, err := Update(matched, func(s *Source) error {
				s.ClassUID = p.ID
				s.Category = p.Category
				s.Path = StorePath(p.Path)
				if s.Vendor == "" && p.Vendor != "" {
					s.Vendor = p.Vendor
				}
				return nil
			}); err != nil {
				return err
			}
		} else if _, err := Add(Source{Kind: KindVST3, Name: p.Name, Path: p.Path, ClassUID: p.ID, Vendor: p.Vendor, Category: p.Category, Enabled: true, Origin: OriginUser}); err != nil {
			return err
		}
	}
	return nil
}
