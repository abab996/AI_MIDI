# 🎹 AI_MIDI — AI-Assisted Music Theory & MIDI Composition

> **English** · **中文** — this README is bilingual; each section appears in both languages.
> 本 README 为中英双语，每个章节均提供两种语言。

**AI_MIDI** is a local-first application that connects **MIDI files** with **OpenAI-compatible LLMs** for AI-assisted music theory learning and composition. It runs entirely on your machine — your API key, your projects, and your conversations never leave your computer.

**AI_MIDI** 是一个本地优先的应用，将 **MIDI 文件**与**兼容 OpenAI API 的大模型**连接起来，用于 AI 辅助的乐理学习与编曲创作。应用完全在本地运行——API Key、项目与对话数据始终留在你的电脑上。

---

## ✨ Features / 功能特性

### 🎼 One-shot Workbench (`/`) / 主工作台（一次性任务）
- **Add Chords / 配和弦** — harmonize a melody into MIDI
- **Translate Lyrics / 翻译歌词** — translate lyrics to fit a vocal melody (Japanese kana support)
- **Design Melisma / 设计转音** — generate vocal ornaments & coloratura
- **Custom Requests / 其他要求** — free-form tasks, optionally output as MIDI

### 💬 Multi-turn Chat (`/chat.html`) / 多轮对话
- Project archive + workbench dual view, powered by **MCP tool calls**: the AI can create, parse, organize, and delete MIDI files in your project
- **Workspace binding** — link a local folder; files sync both ways
- **Undo / Redo** — every file operation is reversible (with recycle bin)
- **Cross-project search** over all conversations
- **Thinking process visualization** — expandable reasoning blocks stream live, collapse instantly when done
- Draft auto-save per project; double theme (parchment / dark blue)

### ⚙️ Settings (`/settings.html`) / 设置
- API Key, Base URL, model, and generation parameters persisted to `settings.json`
- Works with **any OpenAI-compatible provider** (DeepSeek, OpenAI, Gemini, local LLMs…)

---

## 🚀 Quick Start / 快速开始

Requires **Python 3.10+** (uses `X | None` type syntax).

```bash
# 1. Create & activate a virtual environment (recommended)
python -m venv .venv
.venv\Scripts\activate        # Windows
# source .venv/bin/activate   # macOS / Linux

# 2. Install dependencies
pip install -r requirements.txt
```

### Run / 运行

```bash
python main.py                 # pywebview native window (default)
python main.py --browser       # open in your default browser
python main.py --scale 0.9     # adjust native window size ratio
```

> `python run.py` is an alias of `python main.py` kept for historical command compatibility.

> `python run.py` 与 `python main.py` 完全等效（参数原样透传），仅为兼容旧命令习惯保留。

The three pages (`/` workbench, `/chat.html` chat, `/settings.html` settings) navigate via the header links. Each navigation is a full page load — content is always re-read from the server, no stale frontend cache.

三个页面（`/` 主工作台、`/chat.html` 多轮对话、`/settings.html` 设置）通过页头互相跳转；每次跳转都是整页刷新，内容始终从服务器重新加载。

---

## 🔑 Configuration / 配置

### Step 1: Provide an API Key / 填写 API Key

**Never hard-code your key.** Open the **Settings** page and fill in:

**不要把 key 写进代码。** 打开「设置」页填写：

| Field / 字段 | Description / 说明 |
|---|---|
| **API Key** | Your provider key (stored in `settings.json`, git-ignored) |
| **Base URL** | Default `https://api.deepseek.com`; any OpenAI-compatible endpoint works |
| **API Path** | Gemini only, e.g. `/v1beta/openai`; leave empty for standard OpenAI providers |
| **Model** | Default `deepseek-v4-pro`; click "Refresh" to fetch the list from your provider |
| **Reasoning / max tokens** | Generation parameters, optional |

> `settings.json` is git-ignored — your key is **never committed**. A blank template is provided at `settings.example.json`.

> `settings.json` 已被 `.gitignore` 忽略——你的 Key **永远不会被提交**。仓库附有空白模板 `settings.example.json`。

### Step 2: Verify the install / 验证安装

```bash
python -c "import config, get, out, ai_api, server, chat_service; print('OK')"
```

(Imports only — no API calls.)

---

## 📖 Usage Guide / 使用指南

### One-shot tasks / 一次性任务（主工作台）

1. Drag & drop or click to select a MIDI file → click **解析 MIDI** (Parse)
2. Pick a task: **配和弦 / 翻译歌词 / 设计转音 / 其他要求**
3. Fill in lyrics / requirements, adjust BPM & time signature if needed
4. Click **▶ 开始** and watch the live console output
5. Download the result MIDI when done

### Multi-turn chat / 多轮对话

1. Create a project in the archive (**＋ 新建档案**)
2. Describe your goal — e.g. *"Write an 8-bar C major melody with a syncopated rhythm"*
3. The AI reads the theory library (`Library/*.md`) and uses MCP tools to create/organize MIDI files — every step is visible as expandable tool blocks
4. Upload files, bind a workspace folder, undo any operation, search across projects

### Tips / 小贴士

- Switch theme (parchment ⇄ dark blue) via the top-right button — remembered in `localStorage`
- Switching projects auto-saves your draft; it is restored when you return
- Click a tool block's summary to expand/collapse its arguments & result

---

## 🔬 How It Works / 工作原理

### The `note_table` text format / 文本音符格式

MIDI cannot be sent directly to an LLM, so this tool represents notes as plain text:

```
[note: "C4", velocity: "80", start: "1", end: "2"]
```

…means: press **C4**, velocity **80**, lasting from **beat 1** to **beat 2**.

- `get.py` parses MIDI → canonical `note_table` text
- `out.py` converts AI-reply text → MIDI (extremely tolerant regex; handles format noise, enharmonic spellings like B#→C, and note-off ordering)

### Architecture / 架构

```
MIDI file ──get.py──▷ note_table text ──server.py /api/run (SSE)──▷ ai_api.py ──▷ LLM
                                                                     ├─ text only ── output/*.txt
                                                                     └─ note_table ── out.py ── MIDI file

chat.html ──/api/chat (SSE)──▷ chat_service.py
                                 ├─ chat_pipeline.py (LLM tool-calling loop)
                                 └─ mcp_server.py (separate stdio subprocess, operates MIDI files)
                                     ├─ Library/*.md (music theory knowledge base)
                                     └─ projects/<uuid>/ (project persistence)
```

| Module / 模块 | Role / 职责 |
|---|---|
| `main.py` | Entry point: uvicorn thread + pywebview native window (icon, DPI, AppUserModelID) |
| `server.py` | FastAPI app: REST + SSE routes, static frontend mount |
| `chat_service.py` | Chat service layer: MCP subprocess, sessions, workspace, undo, drafts |
| `chat_pipeline.py` | Tool-calling loop: context compression → tools → streaming output |
| `mcp_server.py` | MCP server (stdio, isolated subprocess) exposing MIDI tools |
| `project_manager.py` | Project CRUD, history persistence, cross-project search |
| `ai_api.py` | LLM calls (4 functions), unified retry & parameter stripping |
| `get.py` / `out.py` | MIDI ⇄ note_table converters |
| `config.py` | Central config: paths, defaults, base_url validation, settings I/O |
| `web/` | Static frontend (vanilla JS, blueprint/engineering-drawing style) |

Key design decisions:

- **SSE full-state events** — every chat event carries the complete display list; 25ms throttling drops intermediate frames safely
- **MCP isolation** — the tool server runs as a separate subprocess; a crash never takes down the main service
- **Path-traversal protection** — every file operation (MCP, move, upload, download) validates containment within project directories
- **Dual theme via CSS variables** — `html[data-theme]` switching, remembered in `localStorage`

---

## 📁 Project Structure / 项目结构

```
AI_MIDI/
├── main.py              # Entry: FastAPI + pywebview window
├── run.py               # Alias launcher (equivalent to `python main.py`)
├── server.py            # FastAPI backend: REST + SSE routes, static mount
├── chat_service.py      # Chat service layer (MCP / sessions / drafts / workspace)
├── config.py            # Paths, defaults, base_url validation
├── chat_pipeline.py     # Chat orchestration (tool-calling loop)
├── mcp_server.py        # MCP server (isolated stdio subprocess)
├── project_manager.py   # Projects / history / drafts management
├── ai_api.py            # LLM calls (4 task functions)
├── get.py               # MIDI → note_table text
├── out.py               # note_table text → MIDI
├── web/                 # Static frontend (blueprint style, vanilla JS)
│   ├── index.html       # One-shot workbench
│   ├── chat.html        # Multi-turn chat (archive + workbench)
│   ├── settings.html    # Settings page
│   ├── style.css        # Design tokens & components (dual theme)
│   └── js/              # theme.js / app.js / workbench.js / chat.js / settings.js
├── Library/             # Music theory knowledge base (read by AI via MCP)
├── tests/               # pytest suite (100+ cases)
├── input/in.mid         # Default input MIDI
├── samples/             # Sample MIDI / text files
├── docs/prompts.md      # Prompt templates reference
├── settings.example.json# Blank settings template (no key)
├── app_icon.ico         # App icon
├── splash.png / splash.ico  # Splash images
├── requirements.txt     # Python dependencies
├── RUN.bat              # Windows one-click launcher
└── LICENSE              # Apache License 2.0
```

---

## ✅ Testing / 测试

```bash
python -m pytest
```

100+ test cases cover: chat pipeline (context compression, tool loops, retry/stripping), chat service (upload/delete/undo/workspace/moves), MIDI round-trip parsing, path-traversal security, config validation, and more.

100+ 个测试用例覆盖：对话流水线（上下文压缩、工具循环、重试与参数剥离）、对话服务（上传/删除/撤销/工作区/移动）、MIDI 往返解析、路径穿越安全、配置校验等。

---

## 📦 Packaging / 打包成可执行文件

```bash
pyinstaller --onedir --noconsole --icon=app_icon.ico \
  --collect-all fastapi --collect-all uvicorn --collect-all multipart \
  --collect-all pythonnet \
  --add-data "input;input" --add-data "samples;samples" \
  --add-data "web;web" --add-data "Library;Library" \
  --add-data "splash.png;." --add-data "splash.ico;." \
  --name AI_MIDI main.py
```

Result: `dist\AI_MIDI\AI_MIDI.exe`.

> `--collect-all pythonnet` is required — without it the packaged exe crashes on `import webview`.

> `--collect-all pythonnet` 不可省略：不收集会导致打包后的 exe 在导入 `webview` 时崩溃。

---

## 🔒 Privacy & Security / 隐私与安全

- **API key** is stored only in `settings.json` (git-ignored) — never in code, never in the repository
- **All project data** (conversations, MIDI files) lives under `projects/`, also git-ignored
- **base_url validation** enforces HTTPS (port 443); the key is sent only to the Base URL you configure
- **File operations are sandboxed** — path traversal is rejected at every layer (MCP tools, move, upload, download)
- The repo ships with `settings.example.json` only — no credentials, no personal data, no session records

- **API Key** 仅保存在 `settings.json`（已被 git 忽略）——不写进代码，不进仓库
- **全部项目数据**（对话、MIDI 文件）位于 `projects/`，同样不入库
- **base_url 校验**强制 HTTPS（443 端口）；Key 只发送到你填写的地址
- **文件操作沙箱化**——每一层（MCP 工具、移动、上传、下载）都拒绝路径穿越
- 仓库只附带 `settings.example.json` 空白模板——不含任何凭据、个人数据与会话记录

---

## 📄 License / 许可证

[Apache License 2.0](LICENSE)

---

**AI_MIDI** — compose smarter, theory-first. 🎵
