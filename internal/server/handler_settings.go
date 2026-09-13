package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"aimidi/internal/app"
	"aimidi/internal/config"
	"aimidi/internal/llm"
)

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
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

func (r *Router) handleSettings(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		s := config.LoadSettings()
		writeJSON(w, http.StatusOK, map[string]any{
			"api_key":                   maskAPIKey(s.APIKey),
			"base_url":                  s.BaseURL,
			"api_path":                  s.APIPath,
			"model":                     s.Model,
			"max_tokens":                s.MaxTokens,
			"max_completion_tokens":     s.MaxCompletionTokens,
			"reasoning_effort":          s.ReasoningEffort,
			"thinking_enabled":          s.ThinkingEnabled,
			"transport_resume_on_pause": s.TransportResumeOnPause,
		})

	case http.MethodPut:
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
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "无效的 JSON 请求体")
			return
		}

		if _, err := config.ValidateBaseURL(in.BaseURL, in.APIPath); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
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

		apiKey := strings.TrimSpace(in.APIKey)
		// 掩码值/空值 = 保持已保存的密钥不变（此前表单原样回传会把
		// 密钥悄悄清空；掩码值也可能被回存覆盖真实密钥）
		if apiKey == "" || isMaskedAPIKey(apiKey) {
			apiKey = config.LoadSettings().APIKey
		}

		// 基于已保存配置做覆盖式合并：设置页表单只包含 API/生成参数，
		// 直接新建结构体会把 material_dirs、audio 等未在表单里的字段抹掉
		s := config.LoadSettings()
		s.APIKey = apiKey
		s.BaseURL = strings.TrimSpace(in.BaseURL)
		s.APIPath = strings.TrimSpace(in.APIPath)
		s.Model = strings.TrimSpace(in.Model)
		s.MaxTokens = toIntPtr(in.MaxTokens)
		s.MaxCompletionTokens = toIntPtr(in.MaxCompletionTokens)
		s.ReasoningEffort = strings.TrimSpace(in.ReasoningEffort)
		s.ThinkingEnabled = in.ThinkingEnabled
		if in.TransportResumeOnPause != nil {
			s.TransportResumeOnPause = *in.TransportResumeOnPause
		} /* 未提供（设置页表单已不包含此字段）时保持原值，避免覆盖走带条上的开关 */

		if err := config.SaveSettings(s); err != nil {
			writeError(w, http.StatusInternalServerError, "保存配置失败")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "✓ 配置已保存"})

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// openURLPrefixes /api/open-url 放行的外部链接前缀：本应用自己的 GitHub 仓库
// 与官网（Star 请求弹窗、设置页提交 Issue / 关于页链接），不接受任意 URL
// （防开放跳转——与更新降级通道同思路，URL 语义由前端写死）
var openURLPrefixes = []string{
	"https://github.com/abab996/AI_MIDI",
	"https://aimidi.baimoo.top",
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
	url := strings.TrimSpace(in.URL)
	allowed := false
	for _, prefix := range openURLPrefixes {
		if strings.HasPrefix(url, prefix) {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, http.StatusBadRequest, "链接不在允许范围内")
		return
	}
	if err := openExternal(url); err != nil {
		writeError(w, http.StatusInternalServerError, "打开浏览器失败，请手动访问："+url)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	var apiKey, baseURL, apiPath string

	switch req.Method {
	case http.MethodGet:
		apiKey = req.URL.Query().Get("api_key")
		baseURL = req.URL.Query().Get("base_url")
		apiPath = req.URL.Query().Get("api_path")

	case http.MethodPost:
		var in struct {
			APIKey  string `json:"api_key"`
			BaseURL string `json:"base_url"`
			APIPath string `json:"api_path"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		apiKey = in.APIKey
		baseURL = in.BaseURL
		apiPath = in.APIPath

	default:
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// 掩码值/空值（设置页表单原样回传）= 使用已保存的真实密钥
	if apiKey == "" || isMaskedAPIKey(apiKey) {
		apiKey = config.LoadSettings().APIKey
	}

	models, msg, _ := llm.FetchModels(apiKey, baseURL, apiPath)
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
