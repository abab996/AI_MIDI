package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// resetSettingsCache 清掉进程级配置缓存——测试换了 SettingsFile 之后必须调，
// 否则会命中上一个测试留下的缓存。
func resetSettingsCache() {
	settingsMu.Lock()
	settingsCache = nil
	settingsCacheFile = ""
	settingsMu.Unlock()
}

func TestLegacySettingsMigrateToProvider(t *testing.T) {
	orig := SettingsFile
	t.Cleanup(func() {
		SettingsFile = orig
		settingsMu.Lock()
		settingsCache = nil
		settingsCacheFile = ""
		settingsMu.Unlock()
	})
	dir := t.TempDir()
	SettingsFile = filepath.Join(dir, "settings.json")
	raw := []byte(`{
		"api_key": "sk-legacy-key-1234",
		"base_url": "https://generativelanguage.googleapis.com",
		"api_path": "/v1beta/openai",
		"model": "gemini-2.5-pro"
	}`)
	if err := os.WriteFile(SettingsFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// 换了路径，清掉可能来自其它测试的缓存命中
	settingsMu.Lock()
	settingsCache = nil
	settingsCacheFile = ""
	settingsMu.Unlock()

	loaded := LoadSettings()
	if len(loaded.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(loaded.Providers))
	}
	p := loaded.Providers[0]
	if p.Protocol != ProtocolGemini {
		t.Fatalf("protocol = %s, want gemini", p.Protocol)
	}
	if p.APIKey != "sk-legacy-key-1234" || p.APIPath != "/v1beta/openai" {
		t.Fatalf("migrated provider = %+v", p)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "gemini-2.5-pro" {
		t.Fatalf("models = %+v", p.Models)
	}
	resolved, err := ResolveCall(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "gemini-2.5-pro" || resolved.Protocol != ProtocolGemini {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestSelectActiveRejectsDisabled(t *testing.T) {
	s := DefaultSettings()
	s.Providers[0].Enabled = false
	s.Providers[0].APIKey = "k"
	if err := SelectActive(&s, s.Providers[0].ID, DefaultModel); err == nil {
		t.Fatal("disabled provider should not be selectable")
	}
}

func TestNormalizeAPIPathRejectsTraversal(t *testing.T) {
	if _, err := NormalizeAPIPath("/v1/../../etc"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if _, err := ValidateBaseURL("https://api.openai.com", "/v1/../../x"); err == nil {
		t.Fatal("expected ValidateBaseURL to reject bad api path")
	}
}

func TestMergeMaskedProviderKey(t *testing.T) {
	old := []Provider{{ID: "p1", APIKey: "real-secret-value"}}
	in := []Provider{{ID: "p1", APIKey: "••••cret", Name: "OpenAI", Protocol: ProtocolOpenAI, BaseURL: "https://api.openai.com", Enabled: true}}
	got := MergeProviderKeys(old, in)
	if got[0].APIKey != "real-secret-value" {
		t.Fatalf("key = %q", got[0].APIKey)
	}
}

// 删光供应商后必须保持为空：空列表若被写成"没有 providers 键"，下次加载会
// 退回旧版迁移分支，用残留的扁平字段凭空合成一家供应商——用户刚删掉的供应商
// 会带着密钥复活。这里从"删到空 → 存盘 → 重新加载"整条链路验一遍。
func TestEmptyProviderListStaysEmpty(t *testing.T) {
	orig := SettingsFile
	t.Cleanup(func() {
		SettingsFile = orig
		settingsMu.Lock()
		settingsCache = nil
		settingsCacheFile = ""
		settingsMu.Unlock()
	})
	SettingsFile = filepath.Join(t.TempDir(), "settings.json")
	resetSettingsCache()

	// 先存一份有供应商、有密钥的配置
	s := DefaultSettings()
	s.Providers[0].APIKey = "sk-must-not-come-back"
	s.ActiveProviderID = s.Providers[0].ID
	s.ActiveModelID = s.Providers[0].Models[0].ID
	MirrorActive(&s)
	if err := SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	// 用户在设置页删掉了唯一一家供应商
	loaded := LoadSettings()
	loaded.Providers = []Provider{}
	loaded.ActiveProviderID = ""
	loaded.ActiveModelID = ""
	EnsureActiveSelection(&loaded)
	MirrorActive(&loaded)
	if err := SaveSettings(loaded); err != nil {
		t.Fatal(err)
	}

	// 磁盘上必须留着一个空的 providers 数组
	raw, err := os.ReadFile(SettingsFile)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	provRaw, ok := onDisk["providers"]
	if !ok {
		t.Fatalf("providers 键丢失，settings.json = %s", raw)
	}
	if string(provRaw) == "null" {
		t.Fatalf("providers 被写成 null（等同缺失），settings.json = %s", raw)
	}

	// 重新加载：一家都不能有，密钥也不能从扁平字段回来
	resetSettingsCache()
	again := LoadSettings()
	if len(again.Providers) != 0 {
		t.Fatalf("重启后供应商复活了: %+v", again.Providers)
	}
	if again.APIKey != "" {
		t.Fatalf("重启后扁平 api_key 复活了: %q", again.APIKey)
	}
	if again.ActiveProviderID != "" || again.ActiveModelID != "" {
		t.Fatalf("选中项应保持为空: provider=%q model=%q", again.ActiveProviderID, again.ActiveModelID)
	}
	if _, err := ResolveCall(again); err == nil {
		t.Fatal("没有供应商时 ResolveCall 应该报错，否则会拿残留字段去调用")
	}
}

// 空列表落盘后，扁平镜像字段也要清干净——否则文件被外部改坏（providers 键
// 丢失）时会按旧版配置重新合成出一家带密钥的供应商。
func TestMirrorActiveClearsFlatFieldsWhenNoProvider(t *testing.T) {
	s := Settings{
		APIKey:           "sk-leaked",
		BaseURL:          "https://api.deepseek.com",
		APIPath:          "/v1",
		Model:            "deepseek-v4-pro",
		ActiveProviderID: "p_gone",
	}
	MirrorActive(&s)
	if s.APIKey != "" || s.APIPath != "" || s.Model != "" {
		t.Fatalf("扁平字段未清干净: api_key=%q api_path=%q model=%q", s.APIKey, s.APIPath, s.Model)
	}
}

// 本地推理服务不需要密钥（预设里就是空串），别被 ValidateBaseURL 的 https
// 限制之外再加一道密钥门槛。
func TestRequiresAPIKeyOnlyForRemoteHosts(t *testing.T) {
	remote := []string{
		"https://api.deepseek.com",
		"https://api.openai.com",
		// 前缀匹配 127. 会被这些骗过，必须判成需要密钥
		"https://127.evil.com",
		"https://127.0.0.1.evil.com",
		"https://localhost.evil.com",
	}
	for _, u := range remote {
		if !RequiresAPIKey(u) {
			t.Fatalf("%s 应判为需要密钥", u)
		}
	}
	local := []string{
		"http://127.0.0.1:11434",
		"http://localhost:1234",
		"http://[::1]:8000",
		"127.0.0.1:8080",
		"http://127.0.0.1",
	}
	for _, u := range local {
		if RequiresAPIKey(u) {
			t.Fatalf("%s 应判为本地免密", u)
		}
	}
}

// 思考强度的三态解析：
//   - 没配过档位（nil）→ 沿用模型自己的值，没有就用全局默认（老数据不变行为）
//   - 有启用集 → 用选中的那一档；选中档被关掉了就顺延到还开着的第一档
//   - 启用集为空 → 一档都不发，不能掉回全局默认（否则"全关"关不掉）
func TestReasoningEffortResolution(t *testing.T) {
	probe := func(model ModelConfig, globalEffort string) string {
		t.Helper()
		s := DefaultSettings()
		s.Providers[0].Models = []ModelConfig{model}
		s.Providers[0].Enabled = true
		s.ActiveProviderID = s.Providers[0].ID
		s.ActiveModelID = model.ID
		s.ReasoningEffort = globalEffort
		got, err := ResolveCall(s)
		if err != nil {
			t.Fatal(err)
		}
		return got.ReasoningEffort
	}

	// nil：没配过，沿用既有行为
	if got := probe(ModelConfig{ID: "m"}, "medium"); got != "medium" {
		t.Fatalf("未配过档位时应沿用全局值，得到 %q", got)
	}
	if got := probe(ModelConfig{ID: "m", ReasoningEffort: "low"}, "medium"); got != "low" {
		t.Fatalf("未配过档位时模型自己的值优先，得到 %q", got)
	}

	// 有启用集：只认枚举内的选中档
	if got := probe(ModelConfig{ID: "m", ReasoningEfforts: []string{"low", "max"}, ReasoningEffort: "max"}, "medium"); got != "max" {
		t.Fatalf("应取选中的 max，得到 %q", got)
	}
	if got := probe(ModelConfig{ID: "m", ReasoningEfforts: []string{"low", "max"}, ReasoningEffort: ""}, "medium"); got != "low" {
		t.Fatalf("没选时应退到第一档 low，得到 %q", got)
	}

	// 空启用集：一档都不发
	empty := ModelConfig{ID: "m", ReasoningEfforts: []string{}, ReasoningEffort: ""}
	if got := probe(empty, "max"); got != "" {
		t.Fatalf("档位全关时不该发 reasoning_effort，得到 %q", got)
	}
}

// 关闭某一档时，正选着它的模型要顺延到还开着的档位；
// 全部关掉则把选中值清空（避免留下一个永远选不中的值）。
func TestNormalizeReconcilesEffortSelection(t *testing.T) {
	got := NormalizeProviderList([]Provider{{
		ID: "p", Name: "X", BaseURL: "https://api.deepseek.com", Enabled: true,
		Models: []ModelConfig{{ID: "m", ReasoningEfforts: []string{"max", "low"}, ReasoningEffort: "medium"}},
	}})
	m := got[0].Models[0]
	if len(m.ReasoningEfforts) != 2 || m.ReasoningEfforts[0] != "low" || m.ReasoningEfforts[1] != "max" {
		t.Fatalf("启用集未按规范顺序清洗: %v", m.ReasoningEfforts)
	}
	if m.ReasoningEffort != "low" {
		t.Fatalf("选中档不在启用集里应顺延到第一档，得到 %q", m.ReasoningEffort)
	}

	got = NormalizeProviderList([]Provider{{
		ID: "p", Name: "X", BaseURL: "https://api.deepseek.com", Enabled: true,
		Models: []ModelConfig{{ID: "m", ReasoningEfforts: []string{}, ReasoningEffort: "max"}},
	}})
	if got[0].Models[0].ReasoningEffort != "" {
		t.Fatalf("档位全关后应清空选中值，得到 %q", got[0].Models[0].ReasoningEffort)
	}

	// nil 与空数组是两种状态，清洗时不能把 nil 变成空数组
	got = NormalizeProviderList([]Provider{{
		ID: "p", Name: "X", BaseURL: "https://api.deepseek.com", Enabled: true,
		Models: []ModelConfig{{ID: "m", ReasoningEffort: "max"}},
	}})
	if got[0].Models[0].ReasoningEfforts != nil {
		t.Fatalf("没配过的档位应保持 nil，得到 %#v", got[0].Models[0].ReasoningEfforts)
	}
	if got[0].Models[0].ReasoningEffort != "max" {
		t.Fatalf("没配过档位时不该动选中值，得到 %q", got[0].Models[0].ReasoningEffort)
	}
}

// SetModelThinkingSelection：选档位 = 单独开 thinking + 固定档位；
// "off" = 单独关 thinking（档位值不动）；未启用的档位拒绝。
func TestSetModelThinkingSelection(t *testing.T) {
	s := DefaultSettings()
	s.Providers[0].Models = []ModelConfig{
		{ID: "m", ReasoningEfforts: []string{"low", "medium"}},
	}
	s.Providers[0].Enabled = true

	if err := SetModelThinkingSelection(&s, s.Providers[0].ID, "m", "low", []string{"low", "medium"}); err != nil {
		t.Fatal(err)
	}
	m := &s.Providers[0].Models[0]
	if m.ReasoningEffort != "low" || m.ThinkingEnabled == nil || !*m.ThinkingEnabled {
		t.Fatalf("选档位后 = %+v, want low + thinking true", m)
	}

	if err := SetModelThinkingSelection(&s, s.Providers[0].ID, "m", "off", []string{"low", "medium"}); err != nil {
		t.Fatal(err)
	}
	if m.ReasoningEffort != "low" || m.ThinkingEnabled == nil || *m.ThinkingEnabled {
		t.Fatalf("不思考后 = %+v, want 档位不动 + thinking false", m)
	}

	if err := SetModelThinkingSelection(&s, s.Providers[0].ID, "m", "max", []string{"low", "medium"}); err == nil {
		t.Fatal("未启用的档位应被拒绝")
	}
	if err := SetModelThinkingSelection(&s, s.Providers[0].ID, "nope", "low", []string{"low"}); err == nil {
		t.Fatal("不存在的模型应被拒绝")
	}
}
