package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Provider 一个已添加的 API 供应商。密钥只在本机 settings.json 中明文保存。
type Provider struct {
	ID       string        `json:"id"`
	PresetID string        `json:"preset_id,omitempty"`
	Name     string        `json:"name"`
	Protocol string        `json:"protocol"`
	BaseURL  string        `json:"base_url"`
	APIPath  string        `json:"api_path"`
	APIKey   string        `json:"api_key"`
	Enabled  bool          `json:"enabled"`
	Models   []ModelConfig `json:"models"`
}

// ModelConfig 供应商下已加入「可用模型」的一条。指针字段为 nil 时用「生成参数」页的全局值。
type ModelConfig struct {
	ID                  string `json:"id"`
	MaxTokens           *int   `json:"max_tokens"`
	MaxCompletionTokens *int   `json:"max_completion_tokens"`
	ReasoningEffort     string `json:"reasoning_effort"`
	ThinkingEnabled     *bool  `json:"thinking_enabled"`
	/* ReasoningEfforts 这个模型**启用**了哪些思考强度档位——在设置页逐档开关，
	   只有点亮的档位才会出现在对话页的档位选择里。
	   nil = 还没配过（旧数据），按"跟随全局"处理；空数组 = 用户把所有档位都关了，
	   该模型不发 reasoning_effort。 */
	ReasoningEfforts []string `json:"reasoning_efforts"`
}

// reasoningEffortOrder 档位的规范顺序，界面与接口都按这个顺序展示。
var reasoningEffortOrder = []string{"low", "medium", "max"}

// NormalizeReasoningEfforts 清洗档位集合：只留认识的值、去重、按规范顺序排序。
// 返回 nil 表示"没配过"，与长度为零的"全关了"是两种不同状态，不能混。
func NormalizeReasoningEfforts(list []string) []string {
	if list == nil {
		return nil
	}
	on := map[string]bool{}
	for _, v := range list {
		on[strings.ToLower(strings.TrimSpace(v))] = true
	}
	out := make([]string, 0, len(reasoningEffortOrder))
	for _, lvl := range reasoningEffortOrder {
		if on[lvl] {
			out = append(out, lvl)
		}
	}
	return out
}

// HasReasoningEffort 判断某档位是否在该集合里。
func HasReasoningEffort(list []string, level string) bool {
	for _, v := range list {
		if v == level {
			return true
		}
	}
	return false
}

// ProviderPreset 设置页「从预设添加」使用的模板，不含密钥。
type ProviderPreset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	APIPath  string `json:"api_path"`
}

const (
	maxProviders = 24
	maxModels    = 40
)

// NormalizeProtocol 把协议收成三种之一，无法识别时按 OpenAI Chat。
func NormalizeProtocol(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case ProtocolAnthropic:
		return ProtocolAnthropic
	case ProtocolGemini:
		return ProtocolGemini
	default:
		return ProtocolOpenAI
	}
}

// EffectiveProtocol 优先用调用方显式指定的协议；旧配置只靠主机名识别 Gemini。
func EffectiveProtocol(s Settings) string {
	if p := strings.TrimSpace(s.Protocol); p != "" {
		return NormalizeProtocol(p)
	}
	if IsGeminiProvider(s.BaseURL) {
		return ProtocolGemini
	}
	return ProtocolOpenAI
}

// NewProviderID 生成供应商实例 id。
func NewProviderID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "p_local"
	}
	return "p_" + hex.EncodeToString(b)
}

func providerIDOK(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// NormalizeProviderList 清洗前端或磁盘上的供应商列表。
func NormalizeProviderList(list []Provider) []Provider {
	if len(list) > maxProviders {
		list = list[:maxProviders]
	}
	out := make([]Provider, 0, len(list))
	seenID := map[string]bool{}
	for _, p := range list {
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = "自定义供应商"
		}
		if len([]rune(p.Name)) > 80 {
			p.Name = string([]rune(p.Name)[:80])
		}
		p.Protocol = NormalizeProtocol(p.Protocol)
		p.BaseURL = strings.TrimSpace(p.BaseURL)
		p.APIPath = strings.TrimSpace(p.APIPath)
		p.APIKey = strings.TrimSpace(p.APIKey)
		if len(p.APIKey) > 8192 {
			p.APIKey = p.APIKey[:8192]
		}
		p.PresetID = strings.TrimSpace(p.PresetID)
		if !providerIDOK(p.ID) || seenID[p.ID] {
			p.ID = NewProviderID()
		}
		seenID[p.ID] = true
		p.Models = normalizeModels(p.Models)
		out = append(out, p)
	}
	return out
}

func normalizeModels(models []ModelConfig) []ModelConfig {
	if len(models) > maxModels {
		models = models[:maxModels]
	}
	out := make([]ModelConfig, 0, len(models))
	seen := map[string]bool{}
	for _, m := range models {
		m.ID = strings.TrimSpace(m.ID)
		if m.ID == "" || seen[m.ID] || len(m.ID) > 200 {
			continue
		}
		seen[m.ID] = true
		m.ReasoningEffort = strings.ToLower(strings.TrimSpace(m.ReasoningEffort))
		switch m.ReasoningEffort {
		case "", "low", "medium", "max":
		default:
			m.ReasoningEffort = ""
		}
		m.ReasoningEfforts = NormalizeReasoningEfforts(m.ReasoningEfforts)
		/* 选中档位必须落在启用的档位里：用户在设置页关掉了某一档，而模型正好
		   选着它，就顺延到还开着的第一档；一档都没开就别再留着一个选不中的值。 */
		if m.ReasoningEfforts != nil {
			if len(m.ReasoningEfforts) == 0 {
				m.ReasoningEffort = ""
			} else if !HasReasoningEffort(m.ReasoningEfforts, m.ReasoningEffort) {
				m.ReasoningEffort = m.ReasoningEfforts[0]
			}
		}
		if m.MaxTokens != nil && *m.MaxTokens <= 0 {
			m.MaxTokens = nil
		}
		if m.MaxCompletionTokens != nil && *m.MaxCompletionTokens <= 0 {
			m.MaxCompletionTokens = nil
		}
		out = append(out, m)
	}
	return out
}

// SynthesizeLegacyProvider 把旧的扁平 api_key/base_url/model 收成一条供应商。
func SynthesizeLegacyProvider(s Settings) Provider {
	protocol := ProtocolOpenAI
	presetID := ""
	name := "自定义供应商"
	if pre, ok := PresetByBaseURL(s.BaseURL); ok {
		protocol = pre.Protocol
		presetID = pre.ID
		name = pre.Name
	} else if IsGeminiProvider(s.BaseURL) {
		protocol = ProtocolGemini
		presetID = "gemini"
		name = "Google Gemini"
	}
	model := strings.TrimSpace(s.Model)
	if model == "" {
		model = DefaultModel
	}
	base := strings.TrimSpace(s.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}
	return Provider{
		ID:       "p_legacy",
		PresetID: presetID,
		Name:     name,
		Protocol: protocol,
		BaseURL:  base,
		APIPath:  strings.TrimSpace(s.APIPath),
		APIKey:   s.APIKey,
		Enabled:  true,
		Models:   []ModelConfig{{ID: model}},
	}
}

// ApplyProvidersFromRaw 在 parseSettings 末尾调用：有 providers 用数组，否则从扁平字段迁移。
func ApplyProvidersFromRaw(res *Settings, raw map[string]interface{}) {
	parsed := false
	if rawProv, ok := raw["providers"]; ok && rawProv != nil {
		b, err := json.Marshal(rawProv)
		if err == nil {
			var list []Provider
			if json.Unmarshal(b, &list) == nil {
				res.Providers = NormalizeProviderList(list)
				parsed = true
			}
		}
	}
	if !parsed {
		p := SynthesizeLegacyProvider(*res)
		res.Providers = []Provider{p}
		res.ActiveProviderID = p.ID
		if len(p.Models) > 0 {
			res.ActiveModelID = p.Models[0].ID
		}
	} else {
		if v, ok := raw["active_provider_id"].(string); ok {
			res.ActiveProviderID = strings.TrimSpace(v)
		}
		if v, ok := raw["active_model_id"].(string); ok {
			res.ActiveModelID = strings.TrimSpace(v)
		}
	}
	EnsureActiveSelection(res)
	MirrorActive(res)
}

// EnsureActiveSelection 让选中项落在一条存在的供应商和模型上。
func EnsureActiveSelection(s *Settings) {
	if len(s.Providers) == 0 {
		/* 一条供应商都没有时选中项必须清空：parseSettings 从 DefaultSettings
		   起步，这里不归零的话，"删光供应商"后 settings.json 里会留下指向
		   已不存在供应商的默认 ID（p_default / deepseek-v4-pro） */
		s.ActiveProviderID = ""
		s.ActiveModelID = ""
		return
	}
	idx := -1
	for i := range s.Providers {
		if s.Providers[i].ID == s.ActiveProviderID {
			idx = i
			break
		}
	}
	if idx < 0 {
		idx = 0
		for i := range s.Providers {
			if s.Providers[i].Enabled && len(s.Providers[i].Models) > 0 {
				idx = i
				break
			}
		}
		s.ActiveProviderID = s.Providers[idx].ID
	}
	p := &s.Providers[idx]
	found := false
	for _, m := range p.Models {
		if m.ID == s.ActiveModelID {
			found = true
			break
		}
	}
	if !found && len(p.Models) > 0 {
		s.ActiveModelID = p.Models[0].ID
	}
}

// MirrorActive 把当前选中供应商的连接参数写回扁平字段，旧调用点仍能读到。
// 一条供应商都不剩时把扁平字段一并清干净——留着的话，万一 settings.json 的
// providers 键丢失（被外部改坏、或被老版本程序写回），扁平字段会被当成旧版
// 配置重新合成成一家供应商，等于把密钥又"恢复"了出来。
func MirrorActive(s *Settings) {
	p := findProvider(s, s.ActiveProviderID)
	if p == nil {
		s.APIKey = ""
		s.APIPath = ""
		s.Model = ""
		return
	}
	s.APIKey = p.APIKey
	if p.BaseURL != "" {
		s.BaseURL = p.BaseURL
	}
	s.APIPath = p.APIPath
	if s.ActiveModelID != "" {
		s.Model = s.ActiveModelID
	}
}

func findProvider(s *Settings, id string) *Provider {
	for i := range s.Providers {
		if s.Providers[i].ID == id {
			return &s.Providers[i]
		}
	}
	return nil
}

// SyncLegacyFlat 旧版设置 PUT（没有 providers 字段）只改扁平连接参数，并写回当前供应商。
func SyncLegacyFlat(s *Settings) {
	if len(s.Providers) == 0 {
		p := SynthesizeLegacyProvider(*s)
		s.Providers = []Provider{p}
		s.ActiveProviderID = p.ID
		s.ActiveModelID = s.Model
		return
	}
	p := findProvider(s, s.ActiveProviderID)
	if p == nil {
		p = &s.Providers[0]
		s.ActiveProviderID = p.ID
	}
	p.APIKey = s.APIKey
	if strings.TrimSpace(s.BaseURL) != "" {
		p.BaseURL = strings.TrimSpace(s.BaseURL)
	}
	p.APIPath = strings.TrimSpace(s.APIPath)
	model := strings.TrimSpace(s.Model)
	if model != "" {
		has := false
		for _, m := range p.Models {
			if m.ID == model {
				has = true
				break
			}
		}
		if !has {
			p.Models = append(p.Models, ModelConfig{ID: model})
		}
		s.ActiveModelID = model
	}
}

// MergeProviderKeys 掩码或空密钥表示「保持该 id 已保存的密钥」。
func MergeProviderKeys(old, incoming []Provider) []Provider {
	prev := make(map[string]string, len(old))
	for _, p := range old {
		prev[p.ID] = p.APIKey
	}
	for i := range incoming {
		k := strings.TrimSpace(incoming[i].APIKey)
		if k == "" || strings.HasPrefix(k, "••••") {
			if v, ok := prev[incoming[i].ID]; ok {
				incoming[i].APIKey = v
			} else {
				incoming[i].APIKey = ""
			}
			continue
		}
		incoming[i].APIKey = k
	}
	return incoming
}

// SelectActive 切换对话使用的供应商和模型。关闭的供应商不能被选中。
func SelectActive(s *Settings, providerID, modelID string) error {
	p := findProvider(s, strings.TrimSpace(providerID))
	if p == nil || !p.Enabled {
		return fmt.Errorf("供应商未开启或不存在")
	}
	modelID = strings.TrimSpace(modelID)
	ok := false
	for _, m := range p.Models {
		if m.ID == modelID {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("模型不在该供应商的可用列表中")
	}
	s.ActiveProviderID = p.ID
	s.ActiveModelID = modelID
	MirrorActive(s)
	return nil
}

// SetModelThinkingSelection 对话页一键设定某个模型的思考方式：
// selection = "off" → 单独关闭这个模型的 thinking（界面上叫「不思考」）；
// = 某个启用的档位 → 单独开启 thinking 并固定为该档。
// 不动当前选中的供应商/模型，也不动「生成参数」页的全局默认。
func SetModelThinkingSelection(s *Settings, providerID, modelID, selection string, allowed []string) error {
	p := findProvider(s, strings.TrimSpace(providerID))
	if p == nil {
		return fmt.Errorf("供应商不存在")
	}
	selection = strings.ToLower(strings.TrimSpace(selection))
	if selection != "off" && !HasReasoningEffort(allowed, selection) {
		if len(allowed) == 0 {
			return fmt.Errorf("该模型没有启用的思考强度档位")
		}
		return fmt.Errorf("该模型只启用了这些思考强度：%s", strings.Join(allowed, " / "))
	}
	modelID = strings.TrimSpace(modelID)
	for i := range p.Models {
		if p.Models[i].ID == modelID {
			if selection == "off" {
				off := false
				p.Models[i].ThinkingEnabled = &off
			} else {
				on := true
				p.Models[i].ThinkingEnabled = &on
				p.Models[i].ReasoningEffort = selection
			}
			return nil
		}
	}
	return fmt.Errorf("模型不在该供应商的可用列表中")
}

// ModelReasoningEfforts 返回某条模型当前启用的思考强度档位。
// 第二个返回值是"这个模型配过档位吗"——没配过时调用方回退到模型目录的声明。
func ModelReasoningEfforts(s *Settings, providerID, modelID string) ([]string, bool) {
	p := findProvider(s, strings.TrimSpace(providerID))
	if p == nil {
		return nil, false
	}
	modelID = strings.TrimSpace(modelID)
	for i := range p.Models {
		if p.Models[i].ID == modelID {
			if p.Models[i].ReasoningEfforts == nil {
				return nil, false
			}
			return p.Models[i].ReasoningEfforts, true
		}
	}
	return nil, false
}

// ResolveCall 解析这一轮请求实际使用的供应商、模型和生成参数。
// 没有供应商列表时按扁平字段调用（单测里的 mock）。
func ResolveCall(s Settings) (Settings, error) {
	if len(s.Providers) == 0 {
		s.Protocol = EffectiveProtocol(s)
		if strings.TrimSpace(s.Model) == "" {
			/* 走到这里通常就是"供应商被删光了"——直接说清楚该去哪儿加，
			   别再让人去对话页找那个不存在的模型菜单 */
			return Settings{}, fmt.Errorf("还没有任何供应商，请到设置页「连接参数」添加一个")
		}
		return s, nil
	}
	p := findProvider(&s, s.ActiveProviderID)
	if p == nil || !p.Enabled {
		return Settings{}, fmt.Errorf("请先在对话页选择一个已开启供应商下的可用模型")
	}
	var model *ModelConfig
	for i := range p.Models {
		if p.Models[i].ID == s.ActiveModelID {
			model = &p.Models[i]
			break
		}
	}
	if model == nil {
		return Settings{}, fmt.Errorf("请先在对话页选择一个已开启供应商下的可用模型")
	}
	out := s
	out.APIKey = p.APIKey
	out.BaseURL = p.BaseURL
	out.APIPath = p.APIPath
	out.Model = model.ID
	out.Protocol = NormalizeProtocol(p.Protocol)
	if model.MaxTokens != nil {
		v := *model.MaxTokens
		out.MaxTokens = &v
	}
	if model.MaxCompletionTokens != nil {
		v := *model.MaxCompletionTokens
		out.MaxCompletionTokens = &v
	}
	/* 思考强度三态：
	   - ReasoningEfforts 为 nil：旧数据没配过档位，沿用既有行为（模型自己的值
	     优先，没有就用「生成参数」页的全局值）
	   - 非空：用选中的那一档；选中档不在启用集里（理论上 normalizeModels 已
	     修过，这里兜底）就退到第一档
	   - 空数组：用户把档位全关了，这一路不发 reasoning_effort——不能让它掉回
	     全局值，否则"关掉"根本关不掉 */
	switch {
	case model.ReasoningEfforts == nil:
		if model.ReasoningEffort != "" {
			out.ReasoningEffort = model.ReasoningEffort
		}
	case len(model.ReasoningEfforts) == 0:
		out.ReasoningEffort = ""
	case HasReasoningEffort(model.ReasoningEfforts, model.ReasoningEffort):
		out.ReasoningEffort = model.ReasoningEffort
	default:
		out.ReasoningEffort = model.ReasoningEfforts[0]
	}
	if model.ThinkingEnabled != nil {
		out.ThinkingEnabled = *model.ThinkingEnabled
	}
	return out, nil
}

// PresetByBaseURL 按主机名匹配预设。
func PresetByBaseURL(base string) (ProviderPreset, bool) {
	host := hostnameOf(base)
	if host == "" {
		return ProviderPreset{}, false
	}
	for _, p := range ProviderPresets {
		if strings.EqualFold(hostnameOf(p.BaseURL), host) {
			return p, true
		}
	}
	return ProviderPreset{}, false
}

func hostnameOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func cloneIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	b := *v
	return &b
}

func cloneProviders(list []Provider) []Provider {
	if list == nil {
		return nil
	}
	out := make([]Provider, len(list))
	for i := range list {
		out[i] = list[i]
		if list[i].Models == nil {
			continue
		}
		out[i].Models = make([]ModelConfig, len(list[i].Models))
		for j := range list[i].Models {
			out[i].Models[j] = list[i].Models[j]
			out[i].Models[j].MaxTokens = cloneIntPtr(list[i].Models[j].MaxTokens)
			out[i].Models[j].MaxCompletionTokens = cloneIntPtr(list[i].Models[j].MaxCompletionTokens)
			out[i].Models[j].ThinkingEnabled = cloneBoolPtr(list[i].Models[j].ThinkingEnabled)
		}
	}
	return out
}
