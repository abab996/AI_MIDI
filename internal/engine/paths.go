package engine

import (
	"fmt"
	"path/filepath"
	"strings"
)

// 引擎路径白名单：前端（Wails 绑定与 HTTP 两条通路在 supervisor 汇合）
// 传给引擎的文件路径必须落在约定目录内，防止被注入的内容驱动引擎进程
// 读取/写入任意磁盘路径。
//
// 目录来源经 Config.Dirs 由 main 注入（engine 不能反向依赖 config，
// 见 Config.SoundFontDir 注释）；未注入时 fail-closed：一律拒绝。

// isSubPathFS 判断 target 是否位于 base 目录内（不逃逸出 base）。
// 两侧均解析为绝对路径后做 Rel 判断；Windows 路径大小写不敏感。
func isSubPathFS(base, target string) bool {
	if base == "" || target == "" {
		return false
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	absBase = filepath.Clean(absBase)
	absTarget = filepath.Clean(absTarget)
	if strings.EqualFold(absTarget, absBase) {
		return true
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// mapsFromAny 把 JSON/前端传来的数组统一成 []map[string]any，
// 兼容 []any 与 []map[string]any 两种反序列化形态。
func mapsFromAny(v any) []map[string]any {
	switch arr := v.(type) {
	case []map[string]any:
		return arr
	case []any:
		out := make([]map[string]any, 0, len(arr))
		for _, it := range arr {
			if m, ok := it.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// dirs 返回白名单目录：音色库目录、bounce 输出目录、已注册素材目录。
func (s *Supervisor) dirs() (soundFontDir, outputDir string, materialDirs []string) {
	if s.cfg.Dirs != nil {
		return s.cfg.Dirs()
	}
	return s.cfg.SoundFontDir, "", nil
}

// validateSoundFontPath 音色路径必须位于音色库目录内
func (s *Supervisor) validateSoundFontPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("音色路径为空")
	}
	sfDir, _, _ := s.dirs()
	if sfDir == "" {
		sfDir = s.cfg.SoundFontDir
	}
	if sfDir == "" || !isSubPathFS(sfDir, path) {
		return fmt.Errorf("音色路径不在音色库目录内，已拒绝加载: %s", path)
	}
	return nil
}

// validateMaterialPath 素材路径必须位于已注册素材目录之一
func validateMaterialPath(materialDirs []string, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("素材路径为空")
	}
	for _, dir := range materialDirs {
		if dir != "" && isSubPathFS(dir, path) {
			return nil
		}
	}
	if len(materialDirs) == 0 {
		return fmt.Errorf("尚未注册任何素材目录，已拒绝加载素材: %s", path)
	}
	return fmt.Errorf("素材路径不在已注册的素材目录内，请重新挂载素材后重试: %s", path)
}

// validateClipsPaths 校验扁平化 clips 数组中的 path 字段
func validateClipsPaths(materialDirs []string, clips []map[string]any) error {
	for _, cm := range clips {
		p, _ := cm["path"].(string)
		if strings.TrimSpace(p) == "" {
			continue // 无 path 的 clip 由引擎侧忽略
		}
		if err := validateMaterialPath(materialDirs, p); err != nil {
			return err
		}
	}
	return nil
}

// validateBounceTracksPaths 校验 bounce 原始 tracks 数组中音频 clip 的 src.p
func validateBounceTracksPaths(materialDirs []string, tracks []map[string]any) error {
	for _, tm := range tracks {
		for _, cm := range mapsFromAny(tm["clips"]) {
			typ, _ := cm["type"].(string)
			if typ != "audio" {
				continue
			}
			src, _ := cm["src"].(map[string]any)
			if src == nil {
				continue
			}
			p, _ := src["p"].(string)
			if strings.TrimSpace(p) == "" {
				continue
			}
			if err := validateMaterialPath(materialDirs, p); err != nil {
				return err
			}
		}
	}
	return nil
}
