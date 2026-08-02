# AI_MIDI

基于 **Gradio** 的浏览器应用，把 **MIDI 文件**和 **兼容 OpenAI API 的 LLM** 连接起来，用于 AI 辅助的乐理与编曲任务。

核心流程：`MIDI` → 解析成自定义的 `note_table` 文本格式 → 发送给 LLM（扮演乐理专家）→ 将 AI 回复的文本转回 `MIDI`（或纯文本结果）。

## 功能

通过图形界面选择任务：

1. **配和弦** — 给一段旋律配上和弦，输出为 MIDI。
2. **翻译歌词** — 把歌词翻译成目标语言并贴合人声旋律（日语可附平假名）。
3. **设计转音** — 生成装饰性的转音 / 花腔，输出为 MIDI。
4. **其他要求** — 自由描述任务；可选择输出为 MIDI（`note_table`）或纯文本。

除了上述一次性任务，应用还提供 **多轮对话模式**（通过 MCP 协议让 AI 操作 MIDI 文件，支持上传/解析/创建/删除等工具调用）。

## 安装

需要 Python 3.10+（使用了 `X | None` 类型语法）。

```bash
# 1. 创建并激活虚拟环境（可选但推荐）
python -m venv .venv
.venv\Scripts\activate        # Windows
# source .venv/bin/activate   # macOS / Linux

# 2. 安装依赖
pip install -r requirements.txt
```

## 使用

唯一活跃入口是 `webui.py`。使用 `--browser` 时在系统默认浏览器中打开 Gradio 页面：

```bash
python webui.py --browser
```

不带 `--browser` 时，程序使用 pywebview 原生窗口承载同一页面：

```bash
python webui.py
```

- 支持在设置页配置 Base URL、API 路径 (API Path)、模型、API Key、上下文长度、思考强度等。
- 点击"保存配置"后，设置会持久化到 `settings.json`。
- `config.py` 的 `validate_base_url` 仅强制 https 与 443 端口，不限制域名，任意 OpenAI 兼容服务商均可接入；`api_path`（如 `/v1beta/openai`）仅对 Gemini 服务商生效，其他服务商按标准 OpenAI 格式直接使用 base_url 原路径。

## 配置 API Key

**不要把 key 写进代码。** 首次运行时，在程序的"设置"标签页填写：

- **Base URL**：默认 `https://api.deepseek.com`，可替换为其他兼容 OpenAI API 的服务地址（如 `https://generativelanguage.googleapis.com`）
- **API 路径**：如 Gemini API 可填 `/v1beta/openai`，OpenAI 兼容 API 可填 `/v1` 或留空
- **使用模型**：默认 `deepseek-v4-pro`，可替换为其他模型名称
- **API Key**：你的 API key
- **思考长度 / 上下文长度 / 最大输出长度**：按需调整

填写后点击"保存配置"即可生效，下次启动自动读取。

## 安全提示

- `settings.json` 保存了你的 API Key、Base URL、模型等敏感信息，已列入 `.gitignore`，**不要提交、不要分享**。
- `config.py` 仅强制 base_url 使用 https（443 端口），不限制域名；API Key 只会发送到你填写的 Base URL，请确认该地址可信。

## 打包成可执行文件

项目支持 PyInstaller 打包。**入口为 `webui.py`**：

```bash
pyinstaller --onedir --noconsole --icon=app_icon.ico \
  --collect-all gradio --collect-data safehttpx --collect-data groovy \
  --add-data "input;input" --add-data "samples;samples" \
  --add-data "splash.png;." --add-data "splash.ico;." \
  --name AI_MIDI webui.py
```

打包结果位于 `dist\AI_MIDI\AI_MIDI.exe`。

### 发布前清理

分享程序前，请删除以下包含个人数据或运行产物的文件/目录：

- `settings.json` —— 保存了你的 API Key、Base URL、模型等设置
- `output/` 目录 —— 运行生成的文本 / MIDI 结果
- `output.mid` —— 运行时生成的 MIDI 文件
- `doing/` 目录 —— 解析中间产物
- `projects/` 目录 —— 多轮对话项目数据
- `window_icon.ico` —— 运行时生成的窗口图标

> 这些项目已包含在 `.gitignore` 中，通过 Git 分享源码不会泄露。

## 项目结构

```
AI_MIDI/
├── webui.py             # 唯一活跃入口：Gradio / pywebview 界面
├── config.py            # 路径、默认值与 base_url 白名单
├── webui_components.py  # Gradio 复用组件
├── chat_ui.py           # 多轮对话 Gradio 界面
├── chat_pipeline.py     # 多轮对话流程编排
├── mcp_server.py        # MCP 服务端（供 chat_ui 子进程调用）
├── project_manager.py   # 多轮对话项目 / 会话管理
├── config.py            # 集中配置（路径、默认参数、settings.json 读写）
├── ai_api.py            # LLM 调用（4 个功能：配和弦 / 翻译歌词 / 设计转音 / 其他要求）
├── get.py               # MIDI → note_table 文本解析
├── out.py               # note_table 文本 → MIDI 写入
├── input/in.mid         # 默认输入 MIDI
├── samples/             # 示例 MIDI / 文本
├── docs/prompts.md      # 提示词参考文档
├── app_icon.ico         # 应用图标
├── splash.png / splash.ico  # 启动图
└── settings.json        # 用户设置（API key 等，已 git 忽略）
```

## note_table 格式

由于无法直接上传 MIDI 文件，本工具用文本格式表示音符：

```
[note: "C4", velocity: "80", start: "1", end: "2"]
```

表示按键 C4、力度 80、从第 1 拍持续到第 2 拍。`get.py` 输出的是规范格式，`out.py` 的正则对 AI 回复中的格式噪声极度宽容，但 AI 回复中只应包含这种格式的数据（翻译歌词除外）。

## 验证安装

配置好 key 后，可先验证导入链是否正常：

```bash
python -c "import config, get, out, ai_api, webui; print('OK')"
```

（该命令只导入模块，不触发 API 调用。）
