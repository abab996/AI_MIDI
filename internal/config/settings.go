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

	"melodymate/internal/engine"
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
	// TransportResumeOnPause 暂停后播放光标回退到本次播放的起始位置
	//（关闭时暂停在当前位置，保持原行为）
	TransportResumeOnPause bool `json:"transport_resume_on_pause"`

	/* 该键必须始终写进 settings.json，不能加 omitempty：空列表被省略后，
	   parseSettings 会把它当成"旧版配置"去走扁平字段迁移，凭空合成一家
	   供应商——用户刚删掉的供应商会带着密钥复活 */
	Providers        []Provider `json:"providers"`
	ActiveProviderID string     `json:"active_provider_id,omitempty"`
	ActiveModelID    string     `json:"active_model_id,omitempty"`
	// Protocol 只在 ResolveCall 的返回值里有效，不写入 settings.json。
	Protocol string `json:"-"`
}

var (
	settingsMu sync.RWMutex
	/* 配置缓存：LoadSettings 在每个请求（含每次音频白名单校验）都会调用，
	   此前每次都完整读盘+JSON 解析。缓存 + mtime/size 校验——外部改动仍
	   能被感知（校验开销仅一次 stat），SaveSettings 写穿更新缓存。 */
	settingsCache     *Settings
	settingsCacheFile string
	settingsMod       time.Time
	settingsSize      int64
)

// DefaultSettings 返回填充了系统默认值的配置
func DefaultSettings() Settings {
	p := Provider{
		ID:       "p_default",
		PresetID: "deepseek",
		Name:     "DeepSeek",
		Protocol: ProtocolOpenAI,
		BaseURL:  DefaultBaseURL,
		APIPath:  DefaultAPIPath,
		Enabled:  true,
		Models:   []ModelConfig{{ID: DefaultModel}},
	}
	return Settings{
		APIKey:           "",
		BaseURL:          DefaultBaseURL,
		APIPath:          DefaultAPIPath,
		Model:            DefaultModel,
		ReasoningEffort:  "max",
		ThinkingEnabled:  true,
		Audio:            AudioSettings{EngineEnabled: true},
		Providers:        []Provider{p},
		ActiveProviderID: p.ID,
		ActiveModelID:    DefaultModel,
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
	cp.Providers = cloneProviders(s.Providers)
	return cp
}

// LoadSettings 从 settings.json 加载配置，缺失字段使用默认值。
// 命中缓存（文件 mtime/size 未变）时直接返回，避免每请求读盘解析。
func LoadSettings() Settings {
	if fi, err := os.Stat(SettingsFile); err == nil {
		settingsMu.RLock()
		cached := settingsCache
		match := cached != nil && settingsCacheFile == SettingsFile && fi.ModTime() == settingsMod && fi.Size() == settingsSize
		settingsMu.RUnlock()
		if match {
			return cloneSettings(cached)
		}
	} else {
		// 文件不存在（首启）：同样允许缓存命中，避免重复读盘
		settingsMu.RLock()
		cached := settingsCache
		settingsMu.RUnlock()
		if cached != nil && settingsCacheFile == SettingsFile {
			return cloneSettings(cached)
		}
	}

	settingsMu.Lock()
	defer settingsMu.Unlock()
	// 二次校验：Held Lock 后再次检查，避免 SaveSettings 并发更新后被旧盘覆盖
	if fi, err := os.Stat(SettingsFile); err == nil {
		if settingsCache != nil && settingsCacheFile == SettingsFile && fi.ModTime() == settingsMod && fi.Size() == settingsSize {
			return cloneSettings(settingsCache)
		}
	} else {
		if settingsCache != nil && settingsCacheFile == SettingsFile {
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

// decryptSettingsRaw 把 raw 里 "dpapi:" 前缀的敏感值就地解密为明文。
// 必须在任何解析/迁移/MirrorActive 之前调用：旧版扁平迁移与"激活供应商
// 镜像到顶层"都会拷贝 api_key，晚一步解密就会把密文镜像扩散出去。
func decryptSettingsRaw(raw map[string]interface{}) {
	if v, ok := raw["api_key"].(string); ok {
		raw["api_key"] = UnprotectSecret(v)
	}
	if list, ok := raw["providers"].([]interface{}); ok {
		for _, item := range list {
			if p, ok := item.(map[string]interface{}); ok {
				if v, ok := p["api_key"].(string); ok {
					p["api_key"] = UnprotectSecret(v)
				}
			}
		}
	}
}

// parseSettings 解析 settings.json 内容为 Settings（未提供的字段取默认值）
func parseSettings(data []byte) Settings {
	res := DefaultSettings()
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Error("读取设置文件失败", "err", err)
		return res
	}
	decryptSettingsRaw(raw)

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
	if v, ok := raw["transport_resume_on_pause"].(bool); ok {
		res.TransportResumeOnPause = v
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
	ApplyProvidersFromRaw(&res, raw)

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

	// nil 会序列化成 null，而 parseSettings 把 null 和"键不存在"一视同仁，
	// 都会退回旧版迁移分支——统一写成 []
	if s.Providers == nil {
		s.Providers = []Provider{}
	}

	/* 落盘副本：敏感值只在磁盘上的 JSON 里加密，内存缓存（下方 clone）
	   保持明文——否则缓存里的 dpapi: 密文会污染掩码/回填/合成逻辑 */
	stored := cloneSettings(&s)
	if enc, err := ProtectSecret(stored.APIKey); err == nil {
		stored.APIKey = enc
	} else {
		slog.Warn("API Key 加密失败，已按明文保存", "err", err)
	}
	for i := range stored.Providers {
		if enc, err := ProtectSecret(stored.Providers[i].APIKey); err == nil {
			stored.Providers[i].APIKey = enc
		} else {
			slog.Warn("供应商 API Key 加密失败，已按明文保存", "provider", stored.Providers[i].ID, "err", err)
		}
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}

	tmp := fmt.Sprintf("%s.tmp.%d.%d", SettingsFile, os.Getpid(), time.Now().UnixNano())
	// 0600：settings.json 含 API Key，非 Windows 平台收紧权限兜底
	//（Windows 权限位近似 no-op，机密性由 DPAPI 承担）
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		slog.Error("写入临时设置文件失败", "err", err)
		return err
	}
	// 尝试 fsync 目录（最佳努力，失败不阻断）
	if f, err := os.OpenFile(tmp, os.O_RDONLY, 0600); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}

	if err := os.Rename(tmp, SettingsFile); err != nil {
		_ = os.Remove(tmp)
		slog.Error("原子替换设置文件失败", "err", err)
		return err
	}
	_ = os.Chmod(SettingsFile, 0600)
	if dir, err := os.Open(filepath.Dir(SettingsFile)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}

	// 写穿缓存：保存的即磁盘上的最新状态（深拷贝避免外部后续改动污染缓存）
	if fi, statErr := os.Stat(SettingsFile); statErr == nil {
		cp := cloneSettings(&s)
		settingsCache = &cp
		settingsCacheFile = SettingsFile
		settingsMod = fi.ModTime()
		settingsSize = fi.Size()
	}

	return nil
}

// GetAPIKey 获取当前保存的 API Key
func GetAPIKey() string {
	s := LoadSettings()
	if resolved, err := ResolveCall(s); err == nil && resolved.APIKey != "" {
		return resolved.APIKey
	}
	return s.APIKey
}
