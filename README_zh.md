# 🎹 AI_MIDI — AI 辅助乐理与 MIDI 编曲

> [**English**](README.md) | **中文**

**AI_MIDI** 是一个本地优先的应用，将 **MIDI 文件**与**兼容 OpenAI API 的大模型**连接起来，用于 AI 辅助的乐理学习与编曲创作。应用完全在本地运行——API Key、项目与对话数据始终留在你的电脑上。

---

## ✨ 功能特性

### 🎼 主工作台（`/`，一次性任务）
- **配和弦** — 给一段旋律配上和弦，输出为 MIDI
- **翻译歌词** — 把歌词翻译成目标语言并贴合人声旋律（日语可附平假名）
- **设计转音** — 生成装饰性的转音 / 花腔，输出为 MIDI
- **其他要求** — 自由描述任务；可选择输出为 MIDI 或纯文本

### 💬 多轮对话（`/chat.html`）
- 项目档案库 + 对话工作台双视图，由 **MCP 工具调用**驱动：AI 可以在你的项目中创建、解析、整理、删除 MIDI 文件
- **工作区绑定** — 关联本地文件夹，文件双向同步
- **撤销 / 恢复** — 每个文件操作都可逆（带回收站）
- **跨项目搜索** — 在全部对话中搜索
- **思考过程可视化** — 可折叠的推理块实时流式展示，思考完毕立即收起
- 草稿按项目自动存档；暖纸 / 暗蓝双主题

### ⚙️ 设置（`/settings.html`）
- API Key、Base URL、模型与生成参数持久化到 `settings.json`
- 兼容**任意 OpenAI 兼容服务商**（DeepSeek、OpenAI、Gemini、本地 LLM…）

---

## 🚀 快速开始

需要 **Python 3.10+**（使用了 `X | None` 类型语法）。

```bash
# 1. 创建并激活虚拟环境（可选但推荐）
python -m venv .venv
.venv\Scripts\activate        # Windows
# source .venv/bin/activate   # macOS / Linux

# 2. 安装依赖
pip install -r requirements.txt
```

### 运行

```bash
python main.py                 # pywebview 原生窗口（默认）
python main.py --browser       # 在系统默认浏览器中打开
python main.py --scale 0.9     # 调整原生窗口占屏幕比例
```

> `python run.py` 与 `python main.py` 完全等效（参数原样透传），仅为兼容旧命令习惯保留。

三个页面（`/` 主工作台、`/chat.html` 多轮对话、`/settings.html` 设置）通过页头互相跳转；每次跳转都是整页刷新，内容始终从服务器重新加载，无前端缓存残留。

---

## 🔑 配置

### 第一步：填写 API Key

**不要把 key 写进代码。** 打开「设置」页填写：

| 字段 | 说明 |
|---|---|
| **API Key** | 你的服务商密钥（保存在 `settings.json`，已被 git 忽略） |
| **Base URL** | 默认 `https://api.deepseek.com`；任意 OpenAI 兼容地址均可 |
| **API 路径** | 仅 Gemini 服务商使用，如 `/v1beta/openai`；其他服务商留空 |
| **使用模型** | 默认 `deepseek-v4-pro`；可点「刷新列表」从 API 拉取 |
| **推理努力 / 最大输出** | 生成参数，可选 |

> `settings.json` 已被 `.gitignore` 忽略——你的 Key **永远不会被提交**。仓库附有空白模板 `settings.example.json`。

### 第二步：验证安装

```bash
python -c "import config, get, out, ai_api, server, chat_service; print('OK')"
```

（该命令只导入模块，不触发 API 调用。）

---

## 📖 使用指南

### 一次性任务（主工作台）

1. 拖入或点击选择 MIDI 文件 → 点击「解析 MIDI」
2. 选择任务：**配和弦 / 翻译歌词 / 设计转音 / 其他要求**
3. 填写歌词 / 具体要求，按需调整 BPM 与拍号
4. 点击「▶ 开始」，观看实时控制台输出
5. 完成后下载结果 MIDI

### 多轮对话

1. 在档案库新建项目（**＋ 新建档案**）
2. 描述你的目标——例如：*"写一段 8 小节 C 大调、带切分节奏的旋律"*
3. AI 会先查阅乐理知识库（`Library/*.md`），再通过 MCP 工具创建/整理 MIDI 文件——每一步都以可折叠的工具块展示
4. 可上传文件、绑定工作区文件夹、撤销任意操作、跨项目搜索

### 小贴士

- 右上角切换主题（暖纸 ⇄ 暗蓝），`localStorage` 记忆
- 切换项目时草稿自动存档，返回时自动恢复
- 点击工具块的摘要可展开/收起其参数与结果

---

## 🔬 工作原理

### note_table 文本音符格式

MIDI 文件无法直接发送给大模型，本工具用纯文本表示音符：

```
[note: "C4", velocity: "80", start: "1", end: "2"]
```

表示：按下 **C4**、力度 **80**、从第 **1 拍**持续到第 **2 拍**。

- `get.py` 解析 MIDI → 规范 `note_table` 文本
- `out.py` 把 AI 回复文本转回 MIDI（正则极度宽容：处理格式噪声、B#→C 等价音名、note_off 排序等）

### 架构

```
MIDI file ──get.py──▷ note_table text ──server.py /api/run (SSE)──▷ ai_api.py ──▷ LLM
                                                                     ├─ text only ── output/*.txt
                                                                     └─ note_table ── out.py ── MIDI file

chat.html ──/api/chat (SSE)──▷ chat_service.py
                                 ├─ chat_pipeline.py (LLM 工具调用循环)
                                 └─ mcp_server.py (独立 stdio 子进程，操作 MIDI 文件)
                                     ├─ Library/*.md（乐理知识库）
                                     └─ projects/<uuid>/（项目持久化）
```

| 模块 | 职责 |
|---|---|
| `main.py` | 唯一入口：uvicorn 线程 + pywebview 原生窗口（图标 / DPI / AppUserModelID） |
| `server.py` | FastAPI 应用：REST + SSE 路由、静态前端挂载 |
| `chat_service.py` | 对话服务层：MCP 子进程、会话、工作区、撤销、草稿 |
| `chat_pipeline.py` | 工具调用循环：上下文压缩 → 工具 → 流式输出 |
| `mcp_server.py` | MCP 服务端（stdio 隔离子进程），暴露 MIDI 操作工具 |
| `project_manager.py` | 项目 CRUD、历史持久化、跨项目搜索 |
| `ai_api.py` | LLM 调用（4 个功能），统一重试与参数剥离 |
| `get.py` / `out.py` | MIDI ⇄ note_table 转换 |
| `config.py` | 集中配置：路径、默认值、base_url 校验、设置读写 |
| `web/` | 静态前端（原生 JS，工程图纸 / 蓝图风） |

关键设计决策：

- **SSE 全量状态事件** — 每个对话事件携带完整显示列表；25ms 节流安全丢弃中间帧
- **MCP 隔离** — 工具服务以独立子进程运行，崩溃不会拖垮主服务
- **路径穿越防护** — 每一层文件操作（MCP、移动、上传、下载）都校验目录包含关系
- **CSS 变量双主题** — `html[data-theme]` 切换，localStorage 记忆

---

## 📁 项目结构

```
AI_MIDI/
├── main.py              # 唯一入口：FastAPI + pywebview 窗口
├── run.py               # 兼容启动器（等效 `python main.py`）
├── server.py            # FastAPI 后端：REST + SSE 路由、静态挂载
├── chat_service.py      # 对话服务层（MCP / 会话 / 草稿 / 工作区）
├── config.py            # 路径、默认值、base_url 校验
├── chat_pipeline.py     # 对话流程编排（工具调用循环）
├── mcp_server.py        # MCP 服务端（隔离 stdio 子进程）
├── project_manager.py   # 项目 / 历史 / 草稿管理
├── ai_api.py            # LLM 调用（4 个任务功能）
├── get.py               # MIDI → note_table 文本
├── out.py               # note_table 文本 → MIDI
├── web/                 # 静态前端（蓝图风，原生 JS）
│   ├── index.html       # 主工作台（一次性任务）
│   ├── chat.html        # 多轮对话（档案库 + 工作台）
│   ├── settings.html    # 设置页
│   ├── style.css        # 设计令牌与组件（双主题）
│   └── js/              # theme.js / app.js / workbench.js / chat.js / settings.js
├── Library/             # 乐理知识库（AI 通过 MCP 读取）
├── tests/               # pytest 测试套件（100+ 用例）
├── input/in.mid         # 默认输入 MIDI
├── samples/             # 示例 MIDI / 文本
├── docs/prompts.md      # 提示词参考文档
├── settings.example.json# 空白配置模板（无 Key）
├── app_icon.ico         # 应用图标
├── splash.png / splash.ico  # 启动图
├── requirements.txt     # Python 依赖
├── RUN.bat              # Windows 一键启动脚本
└── LICENSE              # Apache License 2.0
```

---

## ✅ 测试

```bash
python -m pytest
```

100+ 个测试用例覆盖：对话流水线（上下文压缩、工具循环、重试与参数剥离）、对话服务（上传/删除/撤销/工作区/移动）、MIDI 往返解析、路径穿越安全、配置校验等。

---

## 📦 打包成可执行文件

```bash
pyinstaller --onedir --noconsole --icon=app_icon.ico \
  --collect-all fastapi --collect-all uvicorn --collect-all multipart \
  --collect-all pythonnet \
  --add-data "input;input" --add-data "samples;samples" \
  --add-data "web;web" --add-data "Library;Library" \
  --add-data "splash.png;." --add-data "splash.ico;." \
  --name AI_MIDI main.py
```

打包结果位于 `dist\AI_MIDI\AI_MIDI.exe`。

> `--collect-all pythonnet` 不可省略：不收集会导致打包后的 exe 在导入 `webview` 时崩溃。

---

## 🔒 隐私与安全

- **API Key** 仅保存在 `settings.json`（已被 git 忽略）——不写进代码，不进仓库
- **全部项目数据**（对话、MIDI 文件）位于 `projects/`，同样不入库
- **base_url 校验**强制 HTTPS（443 端口）；Key 只发送到你填写的地址
- **文件操作沙箱化**——每一层（MCP 工具、移动、上传、下载）都拒绝路径穿越
- 仓库只附带 `settings.example.json` 空白模板——不含任何凭据、个人数据与会话记录

---

## 📄 许可证

[Apache License 2.0](LICENSE)

---

**AI_MIDI** —— 懂乐理，先思考，再创作。🎵
