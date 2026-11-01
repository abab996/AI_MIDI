package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withTempSettingsFile 换绑 SettingsFile 到临时目录并清缓存
func withTempSettingsFile(t *testing.T) string {
	t.Helper()
	orig := SettingsFile
	t.Cleanup(func() {
		SettingsFile = orig
		resetSettingsCache()
	})
	SettingsFile = filepath.Join(t.TempDir(), "settings.json")
	resetSettingsCache()
	return SettingsFile
}

// TestSecretRoundtrip 加密→落盘→读回：内存值一致（非 Windows 为恒等实现，
// 测试同样成立），Windows 上磁盘必须是 dpapi: 密文。
func TestSecretRoundtrip(t *testing.T) {
	path := withTempSettingsFile(t)

	s := DefaultSettings()
	s.APIKey = "sk-top-secret-123"
	s.Providers[0].APIKey = "sk-provider-key-456"
	if err := SaveSettings(s); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(string(raw), SecretPrefix) {
			t.Fatalf("Windows 上落盘应含 %q 密文前缀", SecretPrefix)
		}
		if strings.Contains(string(raw), "sk-top-secret-123") {
			t.Fatalf("明文不应出现在 settings.json")
		}
	}

	got := LoadSettings()
	if got.APIKey != "sk-top-secret-123" || got.Providers[0].APIKey != "sk-provider-key-456" {
		t.Fatalf("读回值不一致: %q / %q", got.APIKey, got.Providers[0].APIKey)
	}
}

// TestSecretPlaintextMigrates 明文旧值兼容读取，下次保存自动迁移为密文。
func TestSecretPlaintextMigrates(t *testing.T) {
	path := withTempSettingsFile(t)
	legacy := `{"api_key":"sk-legacy-plain","providers":[{"id":"p1","name":"A","protocol":"openai","api_key":"sk-p1-plain","enabled":true,"models":[{"id":"m1"}]}],"active_provider_id":"p1"}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	resetSettingsCache()

	got := LoadSettings()
	// 顶层 api_key 会被 MirrorActive 镜像为激活供应商的密钥（既有迁移语义），
	// 这里断言的是"明文旧值原样读出"而不是被加密值污染
	if got.APIKey != "sk-p1-plain" || got.Providers[0].APIKey != "sk-p1-plain" {
		t.Fatalf("明文旧值应原样读出: %q / %q", got.APIKey, got.Providers[0].APIKey)
	}
	// 原样保存 → 落盘迁移
	if err := SaveSettings(got); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if runtime.GOOS == "windows" && !strings.Contains(string(raw), SecretPrefix) {
		t.Fatalf("保存后应迁移为 %q 密文", SecretPrefix)
	}
	resetSettingsCache()
	again := LoadSettings()
	if again.APIKey != "sk-p1-plain" || again.Providers[0].APIKey != "sk-p1-plain" {
		t.Fatalf("迁移后读回值不一致: %q / %q", again.APIKey, again.Providers[0].APIKey)
	}
}

// TestMaskedWriteBackKeepsOtherProviderSecrets 只改一个供应商时，其他供应商
// 的密文不被破坏（重复保存也不产生二次加密/乱码）。
func TestMaskedWriteBackKeepsOtherProviderSecrets(t *testing.T) {
	withTempSettingsFile(t)

	s := DefaultSettings()
	s.Providers = append(s.Providers, Provider{ID: "p2", Name: "B", Protocol: ProtocolOpenAI, Enabled: true})
	s.Providers[0].APIKey = "sk-provider-one"
	s.Providers[1].APIKey = "sk-provider-two"
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	// 模拟设置页掩码回写：加载（明文）→ 只改顶层 → 再保存 ×2
	cur := LoadSettings()
	cur.APIKey = "sk-new-top"
	if err := SaveSettings(cur); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(LoadSettings()); err != nil {
		t.Fatal(err)
	}

	got := LoadSettings()
	if got.APIKey != "sk-new-top" {
		t.Fatalf("顶层 api_key 应更新: %q", got.APIKey)
	}
	if got.Providers[0].APIKey != "sk-provider-one" || got.Providers[1].APIKey != "sk-provider-two" {
		t.Fatalf("多次保存后供应商密钥不应损坏: %q / %q", got.Providers[0].APIKey, got.Providers[1].APIKey)
	}
}
