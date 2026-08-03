# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# 唯一活跃入口：FastAPI + 静态前端（浏览器模式，开发调试从这进）
python main.py --browser

# 原生窗口模式（pywebview）
python main.py

# 全量测试
python -m pytest
```

## Architecture

前后端分离：FastAPI 后端（`server.py`）提供 REST + SSE 接口并挂载静态前端（`web/`），前端为纯 HTML/CSS/JS（工程图纸 / 技术蓝图风，暖纸 / 暗蓝双主题）。单 pywebview 窗口内页导航（`/` 主工作台、`/chat.html` 多轮对话、`/settings.html` 设置），**页面切换即整页刷新**，内容从服务器重新加载。

一次性任务流程：

```
MIDI file ──get.py──▷ note_table text ──server.py /api/run（SSE）──▷ ai_api.py ──▷ LLM response
                                                                    ├─ text only ── output/*.txt
                                                                    └─ note_table ── out.py ── MIDI file
```

多轮对话流程（通过 `/chat.html` 触发）：

```
chat.html ──/api/chat（SSE）──▷ chat_service.py
                                 ├─ chat_pipeline.py（LLM 工具调用循环）
                                 └─ mcp_server.py（独立子进程，stdio JSON-RPC，操作 MIDI 文件）
                                     ├─ Library/*.md（乐理知识库）
                                     └─ projects/<uuid>/（项目持久化）
```

- **`main.py`** — 唯一入口：argparse（`--browser` / `--scale`）、uvicorn 后台线程、pywebview 原生窗口（图标 / DPI / AppUserModelID）。
- **`server.py`** — FastAPI 应用：设置与模型、MIDI 解析、任务运行（SSE 流）、文件下载（路径校验）、项目 CRUD、对话（SSE 流）、静态挂载。所有业务逻辑委托给下层模块。
- **`chat_service.py`** — 多轮对话服务层（原 `chat_ui.py` 去 Gradio 化）：MCP 子进程管理、会话状态（内存 + `history.json` 水合）、草稿磁盘存档、工作区绑定、文件上传/删除/撤销/下载。`chat_pipeline.py` 通过 `import chat_service as _chat_ui` 复用其接口（`_mcp_list_tools`、`_build_system_prompt`、`_current_project_id` 等）。
- **`chat_pipeline.py`** — 多轮对话核心循环：上下文压缩 → 工具调用 → 流式输出（产出 Gradio 兼容元组，由 `chat_service.chat_stream` 翻译为 SSE 事件）。
- **`mcp_server.py`** — MCP 服务端（stdio 传输），暴露 MIDI 操作工具，以独立子进程运行，崩溃不拖垮主服务。
- **`project_manager.py`** — 项目 CRUD、历史持久化、跨项目搜索、**对话草稿存取**（`projects/<id>/draft.txt`）。
- **`get.py`** — 输入解析：用 `mido` 读取 MIDI，合并所有轨道，输出 note_table 文本列表。
- **`ai_api.py`** — LLM 调用核心：4 个公开函数（add_chord / translate_lyrics / design_melisma / other_requirements），统一通过 `_chat()` 与 OpenAI 兼容 API 通信，集中处理 thinking 模式、reasoning_effort、max_tokens 等参数。
- **`out.py`** — 输出写入：用极度宽容的正则从 AI 回复中提取音符字段，转回 `mido.MidiFile`。
- **`config.py`** — 集中配置：所有路径常量（含 `WEB_DIR`、`SERVER_PORT`、`DRAFT_FILENAME`）、日志系统、API 默认值、settings.json 读写。其他模块永远通过 `from config import ...` 引用。
- **`web/`** — 静态前端：`index.html`（主工作台三栏）、`chat.html`（档案库 / 工作台双视图）、`settings.html`、`style.css`（设计令牌 + 组件，双主题）、`js/`（theme.js 主题切换、app.js 工具库、各页逻辑）。**页面内不嵌 Python 逻辑，全部通过 fetch 调 API**。

## Key Design Decisions

- **抛弃 Gradio**：UI 层为纯静态前端 + FastAPI。AI 流式输出全部走 SSE（`text/event-stream`），前端 `fetch` 逐事件渲染。
- **页面切换即刷新**：三个页面互相跳转是整页导航，所有数据（项目列表 / 设置值 / 文件清单）每次进入都从服务器重读，无前端缓存残留。
- **对话草稿按项目磁盘存档**：切换项目时前端 POST 草稿到 `projects/<id>/draft.txt`，切回时恢复；发送消息后由服务端清除。
- **SSE 事件携带全量状态**：`chat_stream` 每个事件带完整对话显示列表，前端整段替换渲染；事件按 25ms 节流，丢弃中间态不影响正确性。
- **settings.json 持久化**：API Key、模型、生成参数统一存在 `settings.json`，环境变量仅作历史兼容。修改 API 调用逻辑时不要绕开 `config.get_api_key()`。
- **note_table 格式**：`[note: "C4", velocity: "80", start: "1", end: "2"]`，AI 回复中只应包含这种格式的数据（翻译歌词除外）。`out.py` 的正则对格式噪声极度宽容，但 `get.py` 的输出是规范格式。
- **base_url 验证**：`config.py` 的 `validate_base_url` 仅强制 https 与 443 端口，不限制域名。`api_path`（如 `/v1beta/openai`）是 Gemini 专属适配字段，仅对 `generativelanguage.googleapis.com` 拼接，其他服务商一律按标准 OpenAI 格式使用 base_url 原路径。
- **音符名称规范化**：`out.py` 的 `note_name_to_midi_number` 处理了 B#→C、Cb→B、E#→F、Fb→E 等等价音名。
- **MCP 隔离**：`mcp_server.py` 以独立子进程运行（stdio JSON-RPC），AI 通过 `tools/call` 协议触发对 MIDI 文件的操作，进程崩溃不会拖垮主服务。
- **主题系统**：CSS 变量双主题（暖纸默认 / 暗蓝），`html[data-theme]` 切换，localStorage 记忆；View Transitions API 实现切换动画（回退方案为全局过渡类）。
- **文件下载安全**：`server.py` 的 `/api/files/download` 仅允许 `output/`、`doing/`、`projects/` 目录内的文件；聊天文件下载按项目校验文件名。

## Sensitive Data

`settings.json` 包含 API Key，已列入 `.gitignore`。发布/分享前需清理此文件及 `output/`、`doing/`、`projects/`、`output.mid`、`window_icon.ico`。
