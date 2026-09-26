package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// NormalizeAPIPath 校验供应商路径前缀。空字符串表示按协议补版本段；
// "/" 表示路径就在站点根上（不再插入 /v1）。
func NormalizeAPIPath(apiPath string) (string, error) {
	p := strings.TrimSpace(apiPath)
	if p == "" || p == "/" {
		return p, nil
	}
	if strings.Contains(p, "..") || strings.ContainsAny(p, "\\?#% ") {
		return "", fmt.Errorf("api_path 不合法")
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/", nil
	}
	if len(p) > 200 {
		return "", fmt.Errorf("api_path 过长")
	}
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "" || !apiPathSegmentOK(seg) {
			return "", fmt.Errorf("api_path 不合法")
		}
	}
	return p, nil
}

func apiPathSegmentOK(seg string) bool {
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '~' || r == '+' || r == '-':
		default:
			return false
		}
	}
	return true
}

// IsGeminiProvider 判断 base_url 是否指向 Gemini 的 OpenAI 兼容接口
func IsGeminiProvider(baseURL string) bool {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), GeminiBaseURLHost)
}

// IsLocalBaseURL 判断 base_url 是否指向本机。
// 回环判定必须走 IP 解析：前缀匹配 strings.HasPrefix(host, "127.") 会被
// "127.evil.com"、"127.0.0.1.evil.com" 绕过——它们是公网域名，误判为回环后
// 可走明文 http+任意端口，API Key 将发往攻击者主机。
// ValidateBaseURL 与 API Key 校验共用这一个判定，避免两处标准漂移。
func IsLocalBaseURL(baseURL string) bool {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RequiresAPIKey 报告这个接入点是否必须带密钥。本地推理服务（Ollama、
// LM Studio、vLLM、llama.cpp）默认不校验密钥，空密钥是正常配置，不能把
// 预设直接判死。
func RequiresAPIKey(baseURL string) bool {
	return !IsLocalBaseURL(baseURL)
}

// ValidateBaseURL 验证 base_url 并返回规范化后的 URL；不安全时返回错误
func ValidateBaseURL(rawURL string, apiPath string) (string, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", fmt.Errorf("base_url 不能为空")
	}

	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("无效的 URL: %w", err)
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("base_url 缺少主机名")
	}

	isLoopback := IsLocalBaseURL(raw)
	if !isLoopback {
		if u.Scheme != "https" {
			return "", fmt.Errorf("base_url 必须使用 https 协议: %s", u.Scheme)
		}

		port := u.Port()
		if port != "" && port != "443" {
			return "", fmt.Errorf("base_url 端口必须为 443（标准 HTTPS 端口）: %s", port)
		}
	} else {
		if u.Scheme != "https" && u.Scheme != "http" {
			return "", fmt.Errorf("本地 base_url 必须使用 http 或 https 协议: %s", u.Scheme)
		}
	}

	if u.RawQuery != "" {
		return "", fmt.Errorf("base_url 不能包含查询参数: %s", u.RawQuery)
	}

	if u.Fragment != "" {
		return "", fmt.Errorf("base_url 不能包含片段标识符: %s", u.Fragment)
	}

	existingPath := strings.TrimRight(u.Path, "/")
	if existingPath == "/" {
		existingPath = ""
	}

	safePath, pathErr := NormalizeAPIPath(apiPath)
	if pathErr != nil {
		return "", pathErr
	}

	combinedPath := existingPath
	if safePath != "" && safePath != "/" && !strings.HasSuffix(existingPath, safePath) {
		combinedPath = existingPath + safePath
	}

	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}

	if isLoopback && u.Port() != "" {
		return fmt.Sprintf("%s://%s:%s%s", scheme, host, u.Port(), combinedPath), nil
	}

	return fmt.Sprintf("%s://%s%s", scheme, host, combinedPath), nil
}
