# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# 唯一活跃入口：浏览器模式（Gradio 页面，开发调试都从此进）
python webui.py --browser
```

## Architecture

单一入口 `webui.py`（Gradio），所有功能通过 `webui.py` 的 Gradio 界面调用。

一次性任务流程：

```
MIDI file ──get.py──▷ note_table text ──ai_api.py──▷ LLM response
                                              ├─ text only ── output/*.txt
                                              └─ note_table ── out.py ── MIDI file
```

多轮对话流程（通过 `webui.py` 内的"多轮对话"标签页触发）：

```
webui.py ──chat_ui.py（独立 Gradio 端口 7861）──▷ chat_pipeline.py
                                                   ├─ ai_api.py（LLM 工具调用）
                                                   └─ mcp_server.py（子进程，操作 MIDI 文件）
                                                       ├─ Library/*.md（乐理知识库）
                                                       └─ projects/<uuid>/（项目持久化）
```

- **`get.py`** — 输入解析：用 `mido` 读取 MIDI，合并所有轨道，通过 note_on/note_off 对追踪每个音符的起止 tick，换算为拍后输出 note_table 文本列表。
- **`ai_api.py`** — LLM 调用核心：4 个公开函数（add_chord / translate_lyrics / design_melisma / other_requirements），统一通过 `_chat()` 与 OpenAI 兼容 API 通信。`_chat()` 集中处理 thinking 模式、reasoning_effort、max_tokens 等参数。
- **`out.py`** — 输出写入：用极度宽容的正则从 AI 回复中提取音符字段，转回 `mido.MidiFile`。AI 输出格式不规整是常态，正则必须保持宽容。
- **`config.py`** — 集中配置：所有路径常量、日志系统、API 默认值、settings.json 读写均在此。其他模块永远通过 `from config import ...` 引用，不硬编码路径。
- **`webui.py`** — Gradio 界面（唯一活跃入口），含 base_url 域名白名单验证（`_ALLOWED_BASE_URL_DOMAINS`），防止 API Key 被发送到不可信服务。
- **`chat_ui.py`** — 多轮对话 Gradio 界面（独立端口），含项目浏览器（创建/打开/重命名/复制/删除/搜索）。
- **`chat_pipeline.py`** — 多轮对话核心循环：上下文压缩 → 工具调用 → 流式输出。
- **`mcp_server.py`** — MCP 服务端（stdio 传输），暴露 MIDI 操作工具给 chat 子进程。
- **`project_manager.py`** — 多轮对话项目 CRUD、历史持久化、跨项目搜索。
- **`webui_components.py`** — Gradio 复用组件构建函数。

## Key Design Decisions

- **settings.json 持久化**：API Key、模型、生成参数统一存在 `settings.json`，环境变量仅作历史兼容。修改 API 调用逻辑时不要绕开 `config.get_api_key()`。
- **note_table 格式**：`[note: "C4", velocity: "80", start: "1", end: "2"]`，AI 回复中只应包含这种格式的数据（翻译歌词除外）。`out.py` 的正则对格式噪声极度宽容，但 `get.py` 的输出是规范格式。
- **base_url 白名单**：`webui.py` 的 `_ALLOWED_BASE_URL_DOMAINS` 硬编码了 9 个可信域名。新增服务商需同步修改此集合。
- **音符名称规范化**：`out.py` 的 `note_name_to_midi_number` 处理了 B#→C、Cb→B、E#→F、Fb→E 等等价音名。
- **多轮对话的 MCP 隔离**：`mcp_server.py` 以独立子进程运行（stdio JSON-RPC），AI 通过 `tools/call` 协议触发对 MIDI 文件的操作，进程崩溃不会拖垮主 UI。
- **多轮对话上下文压缩**：超出 `MAX_CONTEXT_CHARS` 时自动压缩，保留最近 N 轮对话，替换为简明摘要。

## Sensitive Data

`settings.json` 包含 API Key，已列入 `.gitignore`。发布/分享前需清理此文件及 `output/`、`doing/`、`projects/`、`output.mid`、`window_icon.ico`。
