package llm

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aimidi/internal/config"
)

// 预设里的 Ollama / LM Studio / vLLM / llama.cpp 密钥就是空串，本机服务也
// 不校验密钥。此前 FetchModels 一律硬拒空密钥，这四家永远列不出模型。
func TestFetchModelsAllowsEmptyKeyOnLocalHost(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3.2"},{"id":"qwen3:8b"}]}`))
	}))
	defer srv.Close()

	models, msg, err := FetchModels("", srv.URL, "/v1", config.ProtocolOpenAI)
	if err != nil {
		t.Fatalf("本地服务不该报错: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %v, msg = %q", models, msg)
	}
	// 空密钥时不能发 "Bearer "（裸前缀），有的服务端会因此 400
	if gotAuth != "" {
		t.Fatalf("空密钥不该发 Authorization 头，实际发出 %q", gotAuth)
	}
}

// 反过来：远端主机空密钥仍然要拦住，不能因为放行本地就把校验整个放开。
func TestFetchModelsStillRequiresKeyForRemoteHost(t *testing.T) {
	orig := config.SettingsFile
	t.Cleanup(func() { config.SettingsFile = orig })
	// 指到一个不存在的文件，避免读到开发机上真实的 settings.json
	config.SettingsFile = filepath.Join(t.TempDir(), "settings.json")

	models, msg, err := FetchModels("", "https://api.deepseek.com", "", config.ProtocolOpenAI)
	if err != nil {
		t.Fatalf("空密钥应当返回提示而不是 error: %v", err)
	}
	if models != nil {
		t.Fatalf("不该发起请求，models = %v", models)
	}
	if msg == "" {
		t.Fatal("应当提示去填 API Key")
	}
}

// 目录里明确"不收 reasoning_effort"的模型返回空档位（界面据此隐藏这一栏）；
// 收录且可调的返回目录声明的档位；目录外的按全档位放行——用户手里的模型
// 目录不可能穷举，不该因为没收录就没法调节。
func TestSupportedReasoningEfforts(t *testing.T) {
	if got := SupportedReasoningEfforts("deepseek-v4-pro"); len(got) != 3 {
		t.Fatalf("deepseek-v4-pro 档位 = %v, want 三档", got)
	}
	if got := SupportedReasoningEfforts("o3-mini"); len(got) != 3 {
		t.Fatalf("o3-mini 档位 = %v, want 三档", got)
	}
	if got := SupportedReasoningEfforts("gpt-4o"); len(got) != 0 {
		t.Fatalf("gpt-4o 不该有档位，得到 %v", got)
	}
	if got := SupportedReasoningEfforts("deepseek-chat"); len(got) != 0 {
		t.Fatalf("deepseek-chat 不该有档位，得到 %v", got)
	}
	// 目录外：给全档位
	if got := SupportedReasoningEfforts("some-unknown-model-xyz"); len(got) != 3 {
		t.Fatalf("目录外模型档位 = %v, want 三档", got)
	}
	// 带厂商前缀的写法（OpenRouter 风格）也要能命中目录
	if got := SupportedReasoningEfforts("deepseek/deepseek-v4-pro"); len(got) != 3 {
		t.Fatalf("带前缀的模型档位 = %v, want 三档", got)
	}
}
