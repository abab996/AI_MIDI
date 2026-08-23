package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Settings 用户配置结构
type Settings struct {
	APIKey              string   `json:"api_key"`
	BaseURL             string   `json:"base_url"`
	APIPath             string   `json:"api_path"`
	Model               string   `json:"model"`
	MaxTokens           *int     `json:"max_tokens"`
	MaxCompletionTokens *int     `json:"max_completion_tokens"`
	ReasoningEffort     string   `json:"reasoning_effort"`
	ThinkingEnabled     bool     `json:"thinking_enabled"`
	MaterialDirs        []string `json:"material_dirs,omitempty"`
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
	}
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
			return *cached
		}
	} else {
		// 文件不存在（首启）：同样允许缓存命中，避免重复读盘
		settingsMu.RLock()
		cached := settingsCache
		settingsMu.RUnlock()
		if cached != nil {
			return *cached
		}
	}

	settingsMu.Lock()
	defer settingsMu.Unlock()

	res := DefaultSettings()
	data, err := os.ReadFile(SettingsFile)
	if err == nil {
		res = parseSettings(data)
		// 只在文件存在且可读时更新缓存（缺失/损坏时返回默认值且不缓存，
		// 文件恢复后下一次调用即可读到）
		if fi, statErr := os.Stat(SettingsFile); statErr == nil {
			settingsCache = &res
			settingsMod = fi.ModTime()
			settingsSize = fi.Size()
		}
	}
	return res
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

	_ = os.MkdirAll(filepath.Dir(SettingsFile), 0755)

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp := SettingsFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		slog.Error("写入临时设置文件失败", "err", err)
		return err
	}

	if err := os.Rename(tmp, SettingsFile); err != nil {
		_ = os.Remove(tmp)
		slog.Error("原子替换设置文件失败", "err", err)
		return err
	}

	// 写穿缓存：保存的即磁盘上的最新状态
	if fi, statErr := os.Stat(SettingsFile); statErr == nil {
		saved := s
		settingsCache = &saved
		settingsMod = fi.ModTime()
		settingsSize = fi.Size()
	}

	return nil
}

// GetAPIKey 获取当前保存的 API Key
func GetAPIKey() string {
	return LoadSettings().APIKey
}
