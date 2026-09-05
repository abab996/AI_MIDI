package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		apiPath string
		wantURL string
		wantErr bool
	}{
		{
			name:    "Standard OpenAI URL",
			baseURL: "https://api.openai.com",
			apiPath: "",
			wantURL: "https://api.openai.com",
			wantErr: false,
		},
		{
			name:    "Trailing Slash",
			baseURL: "https://api.openai.com/",
			apiPath: "",
			wantURL: "https://api.openai.com",
			wantErr: false,
		},
		{
			name:    "Gemini with custom api path",
			baseURL: "https://generativelanguage.googleapis.com",
			apiPath: "/v1beta/openai",
			wantURL: "https://generativelanguage.googleapis.com/v1beta/openai",
			wantErr: false,
		},
		{
			name:    "HTTP rejected",
			baseURL: "http://api.openai.com",
			apiPath: "",
			wantErr: true,
		},
		{
			name:    "Custom Port rejected",
			baseURL: "https://api.openai.com:8080",
			apiPath: "",
			wantErr: true,
		},
		{
			name:    "Query parameters rejected",
			baseURL: "https://api.openai.com/v1?key=secret",
			apiPath: "",
			wantErr: true,
		},
		{
			// "127." 前缀匹配曾被当作回环放行 http+任意端口：
			// API Key 会以明文发往攻击者域名，必须按公网主机校验
			name:    "Lookalike 127 domain rejects http",
			baseURL: "http://127.evil.com",
			apiPath: "",
			wantErr: true,
		},
		{
			name:    "Lookalike 127 domain rejects non-443 port",
			baseURL: "https://127.0.0.1.evil.com:8443",
			apiPath: "",
			wantErr: true,
		},
		{
			name:    "Loopback 127.0.0.2 still allows http custom port",
			baseURL: "http://127.0.0.2:11434",
			apiPath: "",
			wantURL: "http://127.0.0.2:11434",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateBaseURL(tc.baseURL, tc.apiPath)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateBaseURL(%q, %q) err = %v, wantErr = %v", tc.baseURL, tc.apiPath, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.wantURL {
				t.Fatalf("ValidateBaseURL(%q, %q) = %q, want %q", tc.baseURL, tc.apiPath, got, tc.wantURL)
			}
		})
	}
}

func TestIsGeminiProvider(t *testing.T) {
	if !IsGeminiProvider("https://generativelanguage.googleapis.com/v1beta/openai") {
		t.Errorf("expected true for generativelanguage.googleapis.com")
	}
	if IsGeminiProvider("https://api.openai.com") {
		t.Errorf("expected false for api.openai.com")
	}
}

func TestSettingsReadWrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "aimidi_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	SettingsFile = filepath.Join(tmpDir, "settings.json")

	maxTok := 4096
	testSettings := Settings{
		APIKey:          "sk-test-key-123456",
		BaseURL:         "https://api.test.com",
		Model:           "test-model",
		MaxTokens:       &maxTok,
		ThinkingEnabled: true,
	}

	if err := SaveSettings(testSettings); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	loaded := LoadSettings()
	if loaded.APIKey != testSettings.APIKey {
		t.Errorf("loaded APIKey = %s, want %s", loaded.APIKey, testSettings.APIKey)
	}
	if loaded.BaseURL != testSettings.BaseURL {
		t.Errorf("loaded BaseURL = %s, want %s", loaded.BaseURL, testSettings.BaseURL)
	}
	if loaded.Model != testSettings.Model {
		t.Errorf("loaded Model = %s, want %s", loaded.Model, testSettings.Model)
	}
	if loaded.MaxTokens == nil || *loaded.MaxTokens != 4096 {
		t.Errorf("loaded MaxTokens = %v, want 4096", loaded.MaxTokens)
	}
	if !loaded.ThinkingEnabled {
		t.Errorf("loaded ThinkingEnabled = false, want true")
	}
}
