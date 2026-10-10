package soundlib

import (
	"melodymate/internal/engine"
	"path/filepath"
	"strings"
)

// SyncVST3Plugins registers scanned instruments as rack sources as well as FX.
// Refresh enriches old identifiers and retains the user's enabled state and name.
func SyncVST3Plugins(scanned []engine.PluginInfo) error {
	pathKey := func(path string) string { return strings.ToLower(filepath.Clean(ResolvePath(path))) }
	counts := map[string]int{}
	for _, p := range scanned {
		counts[pathKey(p.Path)]++
	}
	for _, p := range scanned {
		matched := ""
		for _, old := range List(KindVST3, "") {
			if pathKey(old.Path) == pathKey(p.Path) && (old.ClassUID == p.ID || old.ClassUID != "" && strings.HasSuffix(p.ID, "|"+old.ClassUID) || old.ClassUID == "" && counts[pathKey(p.Path)] == 1) {
				matched = old.ID
				break
			}
		}
		if matched != "" {
			if _, err := Update(matched, func(s *Source) error {
				s.ClassUID = p.ID
				s.Category = p.Category
				s.Path = StorePath(p.Path)
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
