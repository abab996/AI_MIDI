# AI_MIDI

一个把 **MIDI 文件**和 **LLM(DeepSeek)**连接起来的命令行工具,用于完成 AI 辅助的乐理与编曲任务。

核心流程:`MIDI` → 解析成自定义的 `note_table` 文本格式 → 发给 DeepSeek(扮演乐理专家)→ 把 AI 回复的文本转回 `MIDI`(或纯文本结果)。

## 功能

通过交互式菜单选择其一:

1. **配和弦** — 给一段旋律配上和弦,输出为 MIDI。
2. **翻译歌词** — 把歌词翻译成目标语言并贴合人声旋律(日语可附平假名)。
3. **设计转音** — 生成装饰性的转音/花腔,输出为 MIDI。
4. **其他要求** — 自由描述任务;可选择输出为 MIDI(`note_table`)或纯文本。

## 安装

需要 Python 3.10+(使用了 `X | None` 类型语法)。

```bash
# 1. 创建并激活虚拟环境(可选但推荐)
python -m venv .venv
.venv\Scripts\activate        # Windows
# source .venv/bin/activate   # macOS / Linux

# 2. 安装依赖
pip install -r requirements.txt
```

## 配置 API Key

**不要把 key 写进代码。** 通过环境变量提供:

```bash
# 方式 A:设置环境变量(当前会话)
set DEEPSEEK_API_KEY=sk-你的key        # Windows cmd
# export DEEPSEEK_API_KEY=sk-你的key   # macOS / Linux
```

或在本项目根目录创建 `.env` 文件(可复制 `.env.example`):

```
DEEPSEEK_API_KEY=sk-你的key
```

> 注意:程序默认不自动加载 `.env`。如需自动加载,需额外安装 `python-dotenv`
> 并在 `config.py` 顶部调用 `load_dotenv()`。

## 使用

```bash
python main.py
```

把待处理的 MIDI 放到 `input/in.mid`,然后按提示输入 BPM、拍号、歌词、要求等。

- 生成的 MIDI 写到项目根目录 `output.mid`。
- 文本结果(翻译歌词等)写到 `output/` 目录。

## 项目结构

```
AI_MIDI/
├── main.py          # 入口:交互式菜单
├── config.py        # 集中配置(API key、路径、默认参数)
├── ai_api.py        # DeepSeek 调用(4 个功能)
├── get.py           # MIDI → note_table 文本解析
├── out.py           # note_table 文本 → MIDI 写入
├── input/in.mid     # 输入 MIDI
├── output/          # 文本结果(运行时自动创建)
├── doing/           # 解析中间产物
├── docs/prompts.md  # 提示词参考文档
└── samples/         # 历史 MIDI/txt 样本
```

## note_table 格式

由于无法直接上传 MIDI 文件,本工具用文本格式表示音符:

```
[note: "C4", velocity: "80", start: "1", end: "2"]
```

表示按键 C4、力度 80、从第 1 拍持续到第 2 拍。

## 验证安装

配置好 key 后,可先验证导入链是否正常:

```bash
python -c "import config, get, out, ai_api, main; print('OK')"
```

(该命令只导入模块,不触发 API 调用。)
