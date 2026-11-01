//go:build !windows

package config

// 非 Windows 无 DPAPI：敏感值保持明文，由 SaveSettings 把 settings.json
// 与临时文件收紧为 0600 权限兜底（Linux/macOS 桌面单用户场景）。

// SecretPrefix 非 Windows 平台不使用加密前缀（留空便于统一判断逻辑）。
const SecretPrefix = ""

// ProtectSecret 非 Windows：原样返回。
func ProtectSecret(plain string) (string, error) { return plain, nil }

// UnprotectSecret 非 Windows：原样返回。
func UnprotectSecret(stored string) string { return stored }
