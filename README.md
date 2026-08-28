# AI_MIDI · AI 编曲助手

> 对话式 AI 编曲工作台：用自然语言生成 MIDI，在 FL Studio 风格的编曲窗口里编排、试听并导出。

**版本** v3.0.0 · **平台** Windows / Linux · **协议** Apache-2.0（附闭源音频引擎，见 [许可](#许可)）

[English](README_EN.md)

---

## 简介

AI_MIDI 是一个 Windows 桌面应用（Go + Wails v2），把「AI 生成」与「人工编排」放进同一个工作台：

- **对话生成**：用中文描述需求（"写一段 120 BPM 的抒情钢琴旋律"），AI 通过工具调用直接生成 MIDI 文件；支持多轮追问、生成结果撤回、消息编辑重发、任务多线程管理。
- **编曲窗口**：FL Studio Playlist 风格的时间线——轨道头 / Clip 拖拽 / 吸附 / 切分 / 循环区间 / 撤销重做，全部快捷键可配焦点仲裁。
- **钢琴卷帘**：抽屉式 Piano Roll，双击 MIDI Clip 直接编辑音符。
- **素材库**：本地素材目录与工作区双向同步，树状视图浏览、拖拽入轨。
- **输出机架**：FL Studio 风格的浮动机架，通道条拖拽替换轨道音源，SF2 音色库导入（IndexedDB 本地持久化），试听一键分配。
- **原生音频**：可选 JUCE 引擎（ASIO / WASAPI / DirectSound），支持低延迟监听、电平表、离线渲染导出 WAV（尾音到静默）；无引擎时自动降级 Web Audio。

## 架构

```
┌─────────────────────────────── Windows 桌面 ───────────────────────────────┐
│  AI_MIDI.exe (Go + Wails v2)                                              │
│  ├─ frontend/        Vanilla JS 工作台（聊天 / 编曲 / 钢琴卷帘 / 设置）      │
│  ├─ internal/server  本地 HTTP API（仅绑定 127.0.0.1，浏览器模式）           │
│  ├─ internal/chat    多轮对话引擎（流式 SSE、工具调用、历史压缩、撤销）        │
│  ├─ internal/llm     OpenAI 兼容接口客户端（SSE 流式、重试、脱敏）           │
│  ├─ internal/midi    SMF 解析/生成、note_table 换算                         │
│  ├─ internal/project 工程生命周期（清单 / 历史 / 草稿 / 回收站 / 工作区）      │
│  └─ internal/engine  JUCE 引擎守护（命名管道 IPC、心跳、崩溃自动重启）        │
│                                │ 命名管道                                   │
│                     aimidi-engine.exe（闭源，随 Release 附件分发）           │
└───────────────────────────────────────────────────────────────────────────┘
```

音频引擎是独立的 JUCE C++ 工程（**源码私有**，不入本仓库）。缺失时应用自动降级为浏览器 Web Audio 合成，功能可用但延迟较高、离线导出无尾音保障。

## 快速开始

### Windows

- **安装包（推荐）**：从 [Releases](../../releases) 下载 `AI_MIDI_Setup_3.0.0.exe`，双击安装（per-user 免管理员，含中文向导与 JUCE 引擎）。
- **便携版**：下载 `AI_MIDI_v3.0.0_windows_amd64.zip` 解压，双击 `RUN.bat` 启动。
- 首次启动进入设置页，填入 API Key 并保存。

### Linux

1. 从 [Releases](../../releases) 下载 `AI_MIDI_v3.0.0_linux_amd64.tar.gz` 并解压。
2. `chmod +x RUN.sh && ./RUN.sh`（桌面模式需要 webkit2gtk；缺失时用 `./AI_MIDI -browser` 走浏览器模式）。
3. 闭源 JUCE 引擎仅随 Windows 版分发，Linux 自动降级 Web Audio（功能完整，延迟略高）。

### 从源码构建

环境要求：**Go ≥ 1.27**、**Wails v2**（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）、Windows 10+（Linux 见下方说明）。

```bat
git clone https://github.com/abab996/AI_MIDI-go.git
cd AI_MIDI-go
wails build
copy build\bin\AI_MIDI.exe AI_MIDI.exe
RUN.bat
```

> 没有 `aimidi-engine.exe` 时应用照常运行（Web Audio 降级模式），启动日志会给出提示。引擎仅随 Release 分发；持有引擎私有仓库源码者可用 `tools\build_engine.bat` 自行编译。

### 配置

首次运行后，复制 `settings.example.json` 为 `settings.json` 并填入你的密钥（也可在应用设置页里填写）：

```json
{
  "api_key": "sk-你的密钥",
  "base_url": "https://api.deepseek.com",
  "model": "deepseek-v4-pro"
}
```

`settings.json` 含密钥，已被 `.gitignore` 排除，**不要提交**。完整字段：

| 字段 | 说明 | 默认 |
| --- | --- | --- |
| `api_key` | LLM 服务密钥（必填） | — |
| `base_url` | OpenAI 兼容接口地址 | `https://api.deepseek.com` |
| `api_path` | 路径覆盖（留空自动拼接 `/chat/completions`） | 空 |
| `model` | 模型名 | `deepseek-v4-pro` |
| `max_tokens` / `max_completion_tokens` | 输出上限 | 不限 |
| `reasoning_effort` | 推理力度（`low`/`medium`/`high`/`max`） | `max` |
| `thinking_enabled` | 思维链开关（Gemini 系映射 thinking） | `true` |
| `material_dirs` | 素材库扫描目录（可多个） | 空 |
| `transport_resume_on_pause` | 暂停后走带回退到本次起点 | `false` |
| `audio.engine_enabled` | 启用 JUCE 引擎 | `true` |
| `audio.driver` / `audio.device` | 音频驱动 / 输出设备（ASIO 等） | 自动 |
| `audio.sample_rate` / `audio.buffer_size` | 采样率 / 缓冲（引擎延迟估算用） | 48000 / 256 |
| `audio.backend` | `auto`（原生优先）或 `webaudio`（强制浏览器） | `auto` |

## 开发

```bat
wails dev                     ：开发模式（热重载）
wails build                   ：生产构建 → build\bin\AI_MIDI.exe
AI_MIDI.exe -browser          ：浏览器模式（本地 HTTP，127.0.0.1:7860）
set AIMIDI_FRONTEND_DIR=frontend\js\.. && AI_MIDI.exe   ：前端磁盘热更调试
go test ./...                 ：全量单元/E2E 测试
go run tests/run_e2e.go       ：REST API 冒烟（需先启动 -browser 服务）
```

### 目录结构

```
frontend/          前端（Vanilla JS：app/chat/arrangement/pianoroll/engine 模块）
internal/          Go 业务域（chat / llm / midi / mcp / project / server / engine / config / app / tasks）
tests/             E2E（音频管线 / 前端静态断言 / REST 冒烟）
tools/             构建、探针与开发脚本
docs/              设计与决策文档（见下）
```

### 文档索引

- [音频引擎选型与架构方案](docs/音频引擎选型与架构方案.md) — JUCE 选型决策与进程架构
- [JUCE音频引擎开发计划](docs/JUCE音频引擎开发计划.md) — 引擎里程碑与二进制分发合规
- [引擎IPC协议](docs/引擎IPC协议.md) — 主进程 ↔ 引擎命名管道协议 v1
- [编曲窗口设计文档](docs/编曲窗口设计文档.md) — Playlist 式编曲窗口实现基准
- [DAW宿主工程导出开发计划](docs/DAW宿主工程导出开发计划.md) — 导出 FL / Studio One 工程的规划
- [JUCE 8 许可快照](docs/licenses/JUCE-8-许可快照-2026-08-23.md) — 引擎分发依据存档

## 测试

```bat
go test ./...
```

覆盖：MIDI 解析/尾音、会话管线、配置校验、静态资源与来源校验、音频 API、启动图、前端关键静态断言（`tests/frontend_e2e_test.go`）。

## 许可

- 本仓库代码以 [Apache-2.0](LICENSE) 提供。
- `aimidi-engine.exe` 为闭源组件，基于 JUCE 8 构建，随 Release 附件单独分发、不并入源码仓库；其使用受 JUCE 许可条款约束（快照见 docs/licenses/）。
- AI 生成内容与第三方音色（SF2）的版权归各自权利人所有，请在授权范围内使用。

Copyright 2026 AI_MIDI contributors
