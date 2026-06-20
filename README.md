# AI_MIDI

基于 **Gradio + pywebview** 的桌面应用，把 **MIDI 文件**和 **兼容 OpenAI API 的 LLM**连接起来，用于 AI 辅助的乐理与编曲任务。

核心流程：`MIDI` → 解析成自定义的 `note_table` 文本格式 → 发送给 LLM（扮演乐理专家）→ 将 AI 回复的文本转回 `MIDI`（或纯文本结果）。

## 功能

通过图形界面选择任务：

1. **配和弦** — 给一段旋律配上和弦，输出为 MIDI。
2. **翻译歌词** — 把歌词翻译成目标语言并贴合人声旋律（日语可附平假名）。
3. **设计转音** — 生成装饰性的转音 / 花腔，输出为 MIDI。
4. **其他要求** — 自由描述任务；可选择输出为 MIDI（`note_table`）或纯文本。

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

### 桌面应用（推荐）

```bash
python run.py
```

- 显示带透明像素的启动图，随后打开原生窗口。
- 窗口大小自适应 DPI，默认占屏幕工作区的 80%。
- 支持在设置页配置 Base URL、模型、API Key、上下文长度、思考强度等。
- 点击“保存配置”后，设置会持久化到 `settings.json`。

### 浏览器模式

```bash
python webui.py --browser
```

在系统默认浏览器中打开 Gradio 页面，方便调试。

## 配置 API Key

**不要把 key 写进代码。** 首次运行时，在程序的“设置”标签页填写：

- **Base URL**：默认 `https://api.deepseek.com`，可替换为其他兼容 OpenAI API 的服务地址
- **使用模型**：默认 `deepseek-v4-pro`，可替换为其他模型名称
- **API Key**：你的 API key
- **思考长度 / 上下文长度 / 最大输出长度**：按需调整

填写后点击“保存配置”即可生效，下次启动自动读取。

> 如果不想用 UI，也可以通过环境变量提供：
> ```bash
> set DEEPSEEK_API_KEY=sk-你的key        # Windows cmd
> # export DEEPSEEK_API_KEY=sk-你的key   # macOS / Linux
> ```
> 
> 环境变量名保持为 `DEEPSEEK_API_KEY` 以兼容历史配置，但它实际上可对应任意兼容 OpenAI API 的服务。

## 打包成可执行文件

项目支持 PyInstaller 打包为 Windows 桌面程序：

```bash
pyinstaller --onedir --noconsole --icon=app_icon.ico \
  --collect-all gradio --collect-data safehttpx --collect-data groovy \
  --add-data "input;input" --add-data "samples;samples" \
  --add-data "Go.png;." --add-data "splash.png;." --add-data "splash.ico;." \
  --name AI_MIDI run.py
```

打包结果位于 `dist\AI_MIDI\AI_MIDI.exe`。

### 发布前清理

分享程序前，请删除以下包含个人数据或运行产物的文件/目录：

- `settings.json` —— 保存了你的 API Key、Base URL、模型等设置
- `.env`（如果有）—— 可能包含 API Key
- `output/` 目录 —— 运行生成的文本 / MIDI 结果
- `output.mid` —— 运行时生成的 MIDI 文件
- `doing/` 目录 —— 解析中间产物
- `window_icon.ico` —— 运行时生成的窗口图标

> 这些项目已包含在 `.gitignore` 中，通过 Git 分享源码不会泄露。

## 项目结构

```
AI_MIDI/
├── run.py          # 桌面应用入口：启动图 + 原生窗口
├── webui.py        # Gradio 界面 + pywebview 封装
├── config.py       # 集中配置（路径、默认参数）
├── ai_api.py       # DeepSeek API 调用（4 个功能）
├── get.py          # MIDI → note_table 文本解析
├── out.py          # note_table 文本 → MIDI 写入
├── input/in.mid    # 默认输入 MIDI
├── samples/        # 示例 MIDI / 文本
├── docs/prompts.md # 提示词参考文档
└── Go.png          # 启动图（支持透明像素）
```

## note_table 格式

由于无法直接上传 MIDI 文件，本工具用文本格式表示音符：

```
[note: "C4", velocity: "80", start: "1", end: "2"]
```

表示按键 C4、力度 80、从第 1 拍持续到第 2 拍。

## 验证安装

配置好 key 后，可先验证导入链是否正常：

```bash
python -c "import config, get, out, ai_api, webui; print('OK')"
```

（该命令只导入模块，不触发 API 调用。）
