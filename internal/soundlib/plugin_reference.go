package soundlib

import "fmt"

type PluginReference struct {
	SourceID string `json:"sourceId"`
	ID       string `json:"id"`
	Format   string `json:"format"`
	Path     string `json:"path,omitempty"`
	Category string `json:"category"`
}

// ResolvePlugin uses the registered installation, never the order of installed versions.
func ResolvePlugin(sourceID string) (PluginReference, error) {
	src, err := Get(sourceID)
	if err != nil {
		return PluginReference{}, err
	}
	if !src.Enabled {
		return PluginReference{}, fmt.Errorf("插件已禁用：%s", sourceID)
	}
	out := PluginReference{SourceID: src.ID, ID: src.ClassUID, Format: src.Kind, Path: ResolvePath(src.Path), Category: src.Category}
	switch src.Kind {
	case KindMM:
		info, err := InstalledMMInfo(out.Path)
		if err != nil {
			return PluginReference{}, err
		}
		out.ID, _ = info["id"].(string)
		out.Category, _ = info["kind"].(string)
		out.Path, _ = info["libPath"].(string)
		if out.Path == "" {
			return PluginReference{}, fmt.Errorf("MM 插件库缺失：%s", sourceID)
		}
	case KindVST3:
	default:
		return PluginReference{}, fmt.Errorf("该音源不是插件：%s", sourceID)
	}
	return out, nil
}
