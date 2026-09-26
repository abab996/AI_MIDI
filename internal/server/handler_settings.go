package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"aimidi/internal/app"
	"aimidi/internal/config"
	"aimidi/internal/llm"
)

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// maskAPIKey 掩码展示 API Key（明文不再下发给前端——CORS 曾经全开，
// 任意本地页面都能读到完整密钥）
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "••••••••"
	}
	return "••••••••" + key[len(key)-4:]
}

func isMaskedAPIKey(key string) bool {
	return strings.HasPrefix(key, "••••")
}

func settingsPublic(s config.Settings) map[string]any {
	providers := make([]config.Provider, len(s.Providers))
	for i := range s.Providers {
		providers[i] = s.Providers[i]
		providers[i].APIKey = maskAPIKey(s.Providers[i].APIKey)
		if providers[i].Models == nil {
			providers[i].Models = []config.ModelConfig{}
		}
	}
	return map[string]any{
		"api_key":                   maskAPIKey(s.APIKey),
		"base_url":                  s.BaseURL,
		"api_path":                  s.APIPath,
		"model":                     s.Model,
		"max_tokens":                s.MaxTokens,
		"max_completion_tokens":     s.MaxCompletionTokens,
		"reasoning_effort":          s.ReasoningEffort,
		"thinking_enabled":          s.ThinkingEnabled,
		"transport_resume_on_pause": s.TransportResumeOnPause,
		"providers":                 providers,
		"active_provider_id":        s.ActiveProviderID,
		"active_model_id":           s.ActiveModelID,
	}
}

func (r *Router) handleSettings(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, settingsPublic(config.LoadSettings()))

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
			return
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
			return
		}

		var in struct {
			APIKey                 string `json:"api_key"`
			BaseURL                string `json:"base_url"`
			APIPath                string `json:"api_path"`
			Model                  string `json:"model"`
			MaxTokens              any    `json:"max_tokens"`
			MaxCompletionTokens    any    `json:"max_completion_tokens"`
			ReasoningEffort        string `json:"reasoning_effort"`
			ThinkingEnabled        bool   `json:"thinking_enabled"`
			TransportResumeOnPause *bool  `json:"transport_resume_on_pause"`
			ActiveProviderID       string `json:"active_provider_id"`
			ActiveModelID          string `json:"active_model_id"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
			return
		}

		toIntPtr := func(v any) *int {
			if v == nil {
				return nil
			}
			if f, ok := v.(float64); ok {
				i := int(f)
				if i > 0 {
					return &i
				}
			}
			return nil
		}

		s := config.LoadSettings()
		s.MaxTokens = toIntPtr(in.MaxTokens)
		s.MaxCompletionTokens = toIntPtr(in.MaxCompletionTokens)
		if _, ok := raw["reasoning_effort"]; ok {
			s.ReasoningEffort = strings.TrimSpace(in.ReasoningEffort)
		}
		if _, ok := raw["thinking_enabled"]; ok {
			s.ThinkingEnabled = in.ThinkingEnabled
		}
		if in.TransportResumeOnPause != nil {
			s.TransportResumeOnPause = *in.TransportResumeOnPause
		}

		if rawProv, ok := raw["providers"]; ok {
			var list []config.Provider
			if err := json.Unmarshal(rawProv, &list); err != nil {
				writeError(w, http.StatusBadRequest, "供应商列表无效")
				return
			}
			list = config.NormalizeProviderList(list)
			list = config.MergeProviderKeys(s.Providers, list)
			for i := range list {
				if _, err := config.ValidateBaseURL(list[i].BaseURL, list[i].APIPath); err != nil {
					writeError(w, http.StatusBadRequest, list[i].Name+"："+err.Error())
					return
				}
			}
			s.Providers = list
			if _, ok := raw["active_provider_id"]; ok {
				s.ActiveProviderID = strings.TrimSpace(in.ActiveProviderID)
			}
			if _, ok := raw["active_model_id"]; ok {
				s.ActiveModelID = strings.TrimSpace(in.ActiveModelID)
			}
			config.EnsureActiveSelection(&s)
			config.MirrorActive(&s)
		} else {
			if _, err := config.ValidateBaseURL(in.BaseURL, in.APIPath); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			apiKey := strings.TrimSpace(in.APIKey)
			if apiKey == "" || isMaskedAPIKey(apiKey) {
				apiKey = s.APIKey
			}
			s.APIKey = apiKey
			s.BaseURL = strings.TrimSpace(in.BaseURL)
			s.APIPath = strings.TrimSpace(in.APIPath)
			s.Model = strings.TrimSpace(in.Model)
			config.SyncLegacyFlat(&s)
			config.MirrorActive(&s)
		}

		if err := config.SaveSettings(s); err != nil {
			writeError(w, http.StatusInternalServerError, "保存配置失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "✓ 配置已保存"})

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (r *Router) handleSettingsActive(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var in struct {
		ProviderID string `json:"provider_id"`
		ModelID    string `json:"model_id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
		return
	}
	s := config.LoadSettings()
	if err := config.SelectActive(&s, in.ProviderID, in.ModelID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.SaveSettings(s); err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

/*
handleSettingsModelEffort 对话页一键设定某个模型的思考方式，

	不必先把对话切过去。selection = "off"（不思考，模型单独关闭 thinking）
	或一个该模型**启用**的档位（单独开启 thinking 并固定为该档）；启用集在
	设置页的模型卡里逐档开关，模型没配过时按模型目录的声明当作启用集。
*/
func (r *Router) handleSettingsModelEffort(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var in struct {
		ProviderID string `json:"provider_id"`
		ModelID    string `json:"model_id"`
		Selection  string `json:"selection"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
		return
	}
	s := config.LoadSettings()
	allowed, configured := config.ModelReasoningEfforts(&s, in.ProviderID, in.ModelID)
	if !configured {
		allowed = llm.SupportedReasoningEfforts(in.ModelID)
	}
	if err := config.SetModelThinkingSelection(&s, in.ProviderID, in.ModelID, in.Selection, allowed); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.SaveSettings(s); err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (r *Router) handleProviderPresets(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": config.ProviderPresets})
}

func (r *Router) handleModelProfiles(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": llm.ModelProfiles})
}

// openURLAllowHosts /api/open-url 放行的外部链接：本应用自己的 GitHub 仓库
// 与官网（Star 请求弹窗、设置页提交 Issue / 关于页链接），不接受任意 URL
// （防开放跳转——与更新降级通道同思路，URL 语义由前端写死）。
// 校验用 host + 路径前缀（host 精确匹配，路径带边界），天然挡掉
// "github.com/abab996/AI_MIDI.evil.com"（域名吞并）、
// "github.com/abab996/AI_MIDI@evil.com"（userinfo）等前缀绕过变体。
var openURLAllow = []struct {
	host string
	path string
}{
	{"github.com", "/abab996/AI_MIDI"},
	{"aimidi.baimoo.top", ""},
}

// handleOpenURL POST /api/open-url：调起系统默认浏览器打开应用内入口的
// GitHub / 官网链接（openExternal 与更新降级通道同实现）。白名单外返回 400。
func (r *Router) handleOpenURL(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || !allowedOpenURL(parsed) {
		writeError(w, http.StatusBadRequest, "链接不在允许范围内")
		return
	}
	if err := openExternal(parsed.String()); err != nil {
		writeError(w, http.StatusInternalServerError, "打开浏览器失败，请手动访问："+parsed.String())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// allowedOpenURL 校验站点与路径边界：host 必须精确匹配白名单域名，
// 路径必须是白名单路径本身或其子路径（以 / 衔接），且不允许 userinfo
// （"https://site@evil.com" 中有害部分）。
func allowedOpenURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allow := range openURLAllow {
		if host != allow.host {
			continue
		}
		p := u.Path
		if allow.path == "" {
			return true // 官网整站放行
		}
		if p == allow.path {
			return true
		}
		if strings.HasPrefix(p, allow.path+"/") {
			return true
		}
		return false
	}
	return false
}

// handleTransportPrefs 走带偏好：编曲窗走带条上的「暂停后光标回起点」
// 开关直接写这里。独立轻量端点——不走 /api/settings 的表单校验，
// 也避免与设置页整单保存互相干扰
func (r *Router) handleTransportPrefs(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var in struct {
		ResumeOnPause *bool `json:"resume_on_pause"`
	}
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
		return
	}
	if in.ResumeOnPause == nil {
		writeError(w, http.StatusBadRequest, "缺少 resume_on_pause")
		return
	}

	s := config.LoadSettings()
	s.TransportResumeOnPause = *in.ResumeOnPause
	if err := config.SaveSettings(s); err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (r *Router) handleModels(w http.ResponseWriter, req *http.Request) {
	var apiKey, baseURL, apiPath, protocol, providerID string

	switch req.Method {
	case http.MethodGet:
		// 仅用已保存配置拉取模型列表：GET 可被跨站 <img> 无 Origin 触发，
		// 若接受 query 参数指定 base_url，恶意站点可携带已保存的 API Key
		// 请求任意 HTTPS 主机（凭据外泄）。前端刷新模型列表走 POST。
		apiKey = ""
		baseURL = ""
		apiPath = ""

	case http.MethodPost:
		var in struct {
			APIKey     string `json:"api_key"`
			BaseURL    string `json:"base_url"`
			APIPath    string `json:"api_path"`
			Protocol   string `json:"protocol"`
			ProviderID string `json:"provider_id"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		apiKey = in.APIKey
		baseURL = in.BaseURL
		apiPath = in.APIPath
		protocol = in.Protocol
		providerID = in.ProviderID

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// 掩码值/空值（设置页表单原样回传）= 使用已保存的真实密钥
	saved := config.LoadSettings()
	var matched *config.Provider
	for i := range saved.Providers {
		if saved.Providers[i].ID == providerID {
			matched = &saved.Providers[i]
			break
		}
	}
	if apiKey == "" || isMaskedAPIKey(apiKey) {
		if matched != nil && matched.APIKey != "" {
			apiKey = matched.APIKey
		} else {
			apiKey = saved.APIKey
		}
	}
	if protocol == "" && matched != nil {
		protocol = matched.Protocol
	}
	if baseURL == "" && matched != nil {
		baseURL = matched.BaseURL
		apiPath = matched.APIPath
	}

	models, msg, _ := llm.FetchModels(apiKey, baseURL, apiPath, protocol)
	if models == nil {
		models = []string{}
	}
	if msg == "" {
		msg = "✓ 已获取模型列表"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models":  models,
		"message": msg,
	})
}

func (r *Router) handleThemeSwitch(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	theme := req.URL.Query().Get("theme")
	if theme == "" {
		var in struct {
			Theme string `json:"theme"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		theme = in.Theme
	}

	iconFile := "app_icon_dark.ico"
	if theme == "parchment" || theme == "warm" {
		iconFile = "app_icon_warm.ico"
	}

	_ = os.WriteFile(filepath.Join(config.ProjectRoot, "theme.txt"), []byte(theme), 0644)

	app.SetNativeWindowIcon(config.WindowTitle, iconFile)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "theme": theme})
}
