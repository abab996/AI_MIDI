package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

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

	// 回环判定必须走 IP 解析：前缀匹配 strings.HasPrefix(host, "127.")
	// 会被 "127.evil.com"、"127.0.0.1.evil.com" 绕过——它们是公网域名，
	// 被误判为回环后可走明文 http+任意端口，API Key 将发往攻击者主机
	isLoopback := host == "localhost" || host == "::1"
	if !isLoopback {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			isLoopback = true
		}
	}

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

	combinedPath := existingPath
	if host == GeminiBaseURLHost {
		rawAPIPath := strings.TrimSpace(apiPath)
		if rawAPIPath != "" {
			if !strings.HasPrefix(rawAPIPath, "/") {
				rawAPIPath = "/" + rawAPIPath
			}
			rawAPIPath = strings.TrimRight(rawAPIPath, "/")
			combinedPath = existingPath + rawAPIPath
		}
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
