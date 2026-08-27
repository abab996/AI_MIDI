package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aimidi/internal/engine"
)

// AudioSettings 音频设置（与引擎包共享同一类型定义）
type AudioSettings = engine.AudioSettings

// Settings 用户配置结构
type Settings struct {
	APIKey              string        `json:"api_key"`
	BaseURL             string        `json:"base_url"`
	APIPath             string        `json:"api_path"`
	Model               string        `json:"model"`
	MaxTokens           *int          `json:"max_tokens"`
	MaxCompletionTokens *int          `json:"max_completion_tokens"`
	ReasoningEffort     string        `json:"reasoning_effort"`
	ThinkingEnabled     bool          `json:"thinking_enabled"`
	MaterialDirs        []string      `json:"material_dirs,omitempty"`
	Audio               AudioSettings `json:"audio"`
}

var (
	settingsMu sync.RWMutex
	/* 配置缓存：LoadSettings 在每个请求（含每次音频白名单校验）都会调用，
	   此前每次都完整读盘+JSON 解析。缓存 + mtime/size 校验——外部改动仍
	   能被感知（校验开销仅一次 stat），SaveSettings 写穿更新缓存。 */
	settingsCache *Settings
	settingsMod   time.Time
	settingsSize  int64
)

// DefaultSettings 返回填充了系统默认值的配置
func DefaultSettings() Settings {
	return Settings{
		APIKey:          "",
		BaseURL:         DefaultBaseURL,
		APIPath:         DefaultAPIPath,
		Model:           DefaultModel,
		ReasoningEffort: "max",
		ThinkingEnabled: true,
		Audio:           AudioSettings{EngineEnabled: true, Backend: "auto"},
	}
}

func cloneSettings(s *Settings) Settings {
	if s == nil {
		return DefaultSettings()
	}
	cp := *s
	if s.MaterialDirs != nil {
		cp.MaterialDirs = append([]string(nil), s.MaterialDirs...)
	}
	if s.MaxTokens != nil {
		v := *s.MaxTokens
		cp.MaxTokens = &v
	}
	if s.MaxCompletionTokens != nil {
		v := *s.MaxCompletionTokens
		cp.MaxCompletionTokens = &v
	}
	return cp
}

// LoadSettings 从 settings.json 加载配置，缺失字段使用默认值。
// 命中缓存（文件 mtime/size 未变）时直接返回，避免每请求读盘解析。
func LoadSettings() Settings {
	if fi, err := os.Stat(SettingsFile); err == nil {
		settingsMu.RLock()
		cached := settingsCache
		match := cached != nil && fi.ModTime() == settingsMod && fi.Size() == settingsSize
		settingsMu.RUnlock()
		if match {
			return cloneSettings(cached)
		}
	} else {
		// 文件不存在（首启）：同样允许缓存命中，避免重复读盘
		settingsMu.RLock()
		cached := settingsCache
		settingsMu.RUnlock()
		if cached != nil {
			return cloneSettings(cached)
		}
	}

	settingsMu.Lock()
	defer settingsMu.Unlock()
	// 二次校验：Held Lock 后再次检查，避免 SaveSettings 并发更新后被旧盘覆盖
	if fi, err := os.Stat(SettingsFile); err == nil {
		if settingsCache != nil && fi.ModTime() == settingsMod && fi.Size() == settingsSize {
			return cloneSettings(settingsCache)
		}
	} else {
		if settingsCache != nil {
			return cloneSettings(settingsCache)
		}
	}

	res := DefaultSettings()
	data, err := os.ReadFile(SettingsFile)
	if err == nil {
		res = parseSettings(data)
		// 只在文件存在且可读时更新缓存（缺失/损坏时返回默认值且不缓存，
		// 文件恢复后下一次调用即可读到）
		if fi, statErr := os.Stat(SettingsFile); statErr == nil {
			cp := cloneSettings(&res)
			settingsCache = &cp
			settingsMod = fi.ModTime()
			settingsSize = fi.Size()
		}
	}
	return cloneSettings(&res)
}

// parseSettings 解析 settings.json 内容为 Settings（未提供的字段取默认值）
func parseSettings(data []byte) Settings {
	res := DefaultSettings()
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Error("读取设置文件失败", "err", err)
		return res
	}

	if v, ok := raw["api_key"].(string); ok {
		res.APIKey = v
	}
	if v, ok := raw["base_url"].(string); ok && v != "" {
		res.BaseURL = v
	}
	if v, ok := raw["api_path"].(string); ok {
		res.APIPath = v
	}
	if v, ok := raw["model"].(string); ok && v != "" {
		res.Model = v
	}
	if v, ok := raw["reasoning_effort"].(string); ok && v != "" {
		res.ReasoningEffort = v
	}
	if v, ok := raw["thinking_enabled"].(bool); ok {
		res.ThinkingEnabled = v
	}
	if v, ok := raw["material_dirs"].([]interface{}); ok {
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				res.MaterialDirs = append(res.MaterialDirs, filepath.Clean(s))
			}
		}
	}

	if v, ok := raw["audio"].(map[string]interface{}); ok {
		if b, ok := v["engine_enabled"].(bool); ok {
			res.Audio.EngineEnabled = b
		}
		if s, ok := v["engine_path"].(string); ok {
			res.Audio.EnginePath = strings.TrimSpace(s)
		}
		if s, ok := v["driver"].(string); ok {
			res.Audio.Driver = strings.TrimSpace(s)
		}
		if s, ok := v["device"].(string); ok {
			res.Audio.Device = strings.TrimSpace(s)
		}
		if f, ok := v["sample_rate"].(float64); ok && f > 0 {
			res.Audio.SampleRate = int(f)
		}
		if f, ok := v["buffer_size"].(float64); ok && f > 0 {
			res.Audio.BufferSize = int(f)
		}
		if s, ok := v["backend"].(string); ok {
			s = strings.TrimSpace(strings.ToLower(s))
			if s == "webaudio" || s == "auto" {
				res.Audio.Backend = s
			}
		}
		if res.Audio.Backend == "" {
			res.Audio.Backend = "auto"
		}
	}

	toIntPtr := func(val interface{}) *int {
		if val == nil {
			return nil
		}
		if f, ok := val.(float64); ok {
			i := int(f)
			if i > 0 {
				return &i
			}
		}
		return nil
	}

	res.MaxTokens = toIntPtr(raw["max_tokens"])
	res.MaxCompletionTokens = toIntPtr(raw["max_completion_tokens"])

	return res
}

// SaveSettings 将配置原子写入 settings.json（写穿更新缓存）
func SaveSettings(s Settings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(SettingsFile), 0755); err != nil {
		slog.Error("创建设置目录失败", "err", err)
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp := fmt.Sprintf("%s.tmp.%d.%d", SettingsFile, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		slog.Error("写入临时设置文件失败", "err", err)
		return err
	}
	// 尝试 fsync 目录（最佳努力，失败不阻断）
	if f, err := os.OpenFile(tmp, os.O_RDONLY, 0644); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}

	if err := os.Rename(tmp, SettingsFile); err != nil {
		_ = os.Remove(tmp)
		slog.Error("原子替换设置文件失败", "err", err)
		return err
	}
	if dir, err := os.Open(filepath.Dir(SettingsFile)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}

	// 写穿缓存：保存的即磁盘上的最新状态（深拷贝避免外部后续改动污染缓存）
	if fi, statErr := os.Stat(SettingsFile); statErr == nil {
		cp := cloneSettings(&s)
		settingsCache = &cp
		settingsMod = fi.ModTime()
		settingsSize = fi.Size()
	}

	return nil
}

// GetAPIKey 获取当前保存的 API Key
func GetAPIKey() string {
	return LoadSettings().APIKey
}
