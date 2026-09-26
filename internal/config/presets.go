package config

// ProviderPresets 常用与小众供应商模板。Base URL 只含协议和主机，
// API 路径是前缀；空路径表示按协议补 /v1，"/" 表示接在站点根上。
// 账户专属地址（Azure 资源名、Bedrock 区域）不放进预设，用自定义供应商填写。
var ProviderPresets = []ProviderPreset{
	{ID: "deepseek", Name: "DeepSeek", Group: "常用", Protocol: ProtocolOpenAI, BaseURL: "https://api.deepseek.com", APIPath: ""},
	{ID: "openai", Name: "OpenAI", Group: "常用", Protocol: ProtocolOpenAI, BaseURL: "https://api.openai.com", APIPath: "/v1"},
	{ID: "anthropic", Name: "Anthropic", Group: "常用", Protocol: ProtocolAnthropic, BaseURL: "https://api.anthropic.com", APIPath: "/v1"},
	{ID: "gemini", Name: "Google Gemini", Group: "常用", Protocol: ProtocolGemini, BaseURL: "https://generativelanguage.googleapis.com", APIPath: "/v1beta/openai"},
	{ID: "xai", Name: "xAI", Group: "常用", Protocol: ProtocolOpenAI, BaseURL: "https://api.x.ai", APIPath: "/v1"},
	{ID: "mistral", Name: "Mistral", Group: "常用", Protocol: ProtocolOpenAI, BaseURL: "https://api.mistral.ai", APIPath: "/v1"},
	{ID: "groq", Name: "Groq", Group: "常用", Protocol: ProtocolOpenAI, BaseURL: "https://api.groq.com", APIPath: "/openai/v1"},
	{ID: "cohere", Name: "Cohere", Group: "常用", Protocol: ProtocolOpenAI, BaseURL: "https://api.cohere.ai", APIPath: "/compatibility/v1"},

	{ID: "moonshot", Name: "月之暗面 Kimi", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://api.moonshot.cn", APIPath: "/v1"},
	{ID: "moonshot-intl", Name: "Moonshot 国际", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://api.moonshot.ai", APIPath: "/v1"},
	{ID: "zhipu", Name: "智谱 GLM", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://open.bigmodel.cn", APIPath: "/api/paas/v4"},
	{ID: "dashscope", Name: "阿里云百炼", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://dashscope.aliyuncs.com", APIPath: "/compatible-mode/v1"},
	{ID: "dashscope-intl", Name: "百炼国际", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://dashscope-intl.aliyuncs.com", APIPath: "/compatible-mode/v1"},
	{ID: "volcengine", Name: "火山方舟", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://ark.cn-beijing.volces.com", APIPath: "/api/v3"},
	{ID: "minimax", Name: "MiniMax", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://api.minimaxi.com", APIPath: "/v1"},
	{ID: "minimax-intl", Name: "MiniMax 国际", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://api.minimax.io", APIPath: "/v1"},
	{ID: "stepfun", Name: "阶跃星辰", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://api.stepfun.com", APIPath: "/v1"},
	{ID: "qianfan", Name: "百度千帆", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://qianfan.baidubce.com", APIPath: "/v2"},
	{ID: "siliconflow", Name: "硅基流动", Group: "国内", Protocol: ProtocolOpenAI, BaseURL: "https://api.siliconflow.cn", APIPath: "/v1"},

	{ID: "openrouter", Name: "OpenRouter", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://openrouter.ai", APIPath: "/api/v1"},
	{ID: "together", Name: "Together", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.together.xyz", APIPath: "/v1"},
	{ID: "fireworks", Name: "Fireworks", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.fireworks.ai", APIPath: "/inference/v1"},
	{ID: "cerebras", Name: "Cerebras", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.cerebras.ai", APIPath: "/v1"},
	{ID: "sambanova", Name: "SambaNova", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.sambanova.ai", APIPath: "/v1"},
	{ID: "deepinfra", Name: "DeepInfra", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.deepinfra.com", APIPath: "/v1/openai"},
	{ID: "novita", Name: "Novita", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.novita.ai", APIPath: "/openai/v1"},
	{ID: "nebius", Name: "Nebius", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.studio.nebius.com", APIPath: "/v1"},
	{ID: "huggingface", Name: "Hugging Face", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://router.huggingface.co", APIPath: "/v1"},
	{ID: "nvidia", Name: "NVIDIA", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://integrate.api.nvidia.com", APIPath: "/v1"},
	{ID: "featherless", Name: "Featherless", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.featherless.ai", APIPath: "/v1"},
	{ID: "chutes", Name: "Chutes", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://llm.chutes.ai", APIPath: "/v1"},
	{ID: "perplexity", Name: "Perplexity", Group: "聚合", Protocol: ProtocolOpenAI, BaseURL: "https://api.perplexity.ai", APIPath: "/"},

	{ID: "ollama", Name: "Ollama 本地", Group: "本地", Protocol: ProtocolOpenAI, BaseURL: "http://127.0.0.1:11434", APIPath: "/v1"},
	{ID: "lmstudio", Name: "LM Studio", Group: "本地", Protocol: ProtocolOpenAI, BaseURL: "http://127.0.0.1:1234", APIPath: "/v1"},
	{ID: "vllm", Name: "vLLM", Group: "本地", Protocol: ProtocolOpenAI, BaseURL: "http://127.0.0.1:8000", APIPath: "/v1"},
	{ID: "llamacpp", Name: "llama.cpp", Group: "本地", Protocol: ProtocolOpenAI, BaseURL: "http://127.0.0.1:8080", APIPath: "/v1"},
}
