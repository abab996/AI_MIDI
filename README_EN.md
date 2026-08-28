# AI_MIDI · AI Music Arrangement Assistant

> A conversational AI arrangement workbench: generate MIDI with natural language, then arrange, audition and export it in an FL Studio–style arrangement window.

**Version** v3.0.0 · **Platform** Windows · **License** Apache-2.0 (bundled closed-source audio engine, see [License](#license))

[中文说明](README.md)

---

## Overview

AI_MIDI is a Windows desktop app (Go + Wails v2) that puts "AI generation" and "human arrangement" in one workbench:

- **Conversational generation** — describe what you want ("write a 120 BPM lyrical piano melody") and the AI generates MIDI files through tool calls. Multi-turn follow-ups, undo of AI changes, message editing/re-send, and multi-task management are supported.
- **Arrangement window** — an FL Studio Playlist–style timeline: track headers, clip dragging, snapping, split, loop range, undo/redo, with full keyboard shortcuts and focus arbitration.
- **Piano roll** — drawer-style Piano Roll; double-click any MIDI clip to edit its notes.
- **Material library** — bidirectional sync with local material folders and bound workspaces; tree view browsing and drag-to-track.
- **Output rack** — an FL-style floating rack: drag channel strips onto tracks to swap sound sources, import SF2 soundfonts (persisted in IndexedDB), audition and assign with one click.
- **Native audio** — an optional JUCE engine (ASIO / WASAPI / DirectSound) with low-latency monitoring, level meters and offline WAV bounce (tail decay to silence). Without the engine the app falls back to Web Audio automatically.

## Architecture

```
┌──────────────────────────── Windows desktop ────────────────────────────┐
│  AI_MIDI.exe (Go + Wails v2)                                            │
│  ├─ frontend/        Vanilla JS workbench (chat / arrangement / piano)  │
│  ├─ internal/server  Local HTTP API (binds 127.0.0.1, browser mode)     │
│  ├─ internal/chat    Conversation engine (SSE streaming, tools, undo)   │
│  ├─ internal/llm     OpenAI-compatible client (streaming, retry)        │
│  ├─ internal/midi    SMF parse/write, note_table conversion             │
│  ├─ internal/project Project lifecycle (manifest/history/trash/backup)  │
│  └─ internal/engine  JUCE engine supervisor (named-pipe IPC, restart)   │
│                              │ named pipe                                │
│                   aimidi-engine.exe (closed-source, Release asset)      │
└─────────────────────────────────────────────────────────────────────────┘
```

The audio engine is a separate JUCE C++ project whose **sources are private** (not in this repository). When it is missing, the app falls back to browser Web Audio synthesis — fully usable, but with higher latency and no offline-bounce tail guarantee.

## Quick Start

### Option 1: Download a prebuilt package (recommended)

1. Grab the latest `AI_MIDI_v3.0.0_windows_amd64.zip` from [Releases](../../releases) (includes the main program and `aimidi-engine.exe`) and unzip it.
2. Run `RUN.bat`.
3. On first launch, open Settings and enter your API key.

### Option 2: Build from source

Requirements: **Go ≥ 1.27**, **Wails v2** (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`), Windows 10+.

```bat
git clone https://github.com/abab996/AI_MIDI-go.git
cd AI_MIDI-go
wails build
copy build\bin\AI_MIDI.exe AI_MIDI.exe
RUN.bat
```

> Without `aimidi-engine.exe` the app still runs (Web Audio fallback mode); the startup log will tell you. The engine is distributed only as a Release asset. Holders of the private engine repository can build it with `tools\build_engine.bat`.

### Configuration

After the first run, copy `settings.example.json` to `settings.json` and fill in your key (or use the in-app settings page):

```json
{
  "api_key": "sk-your-key",
  "base_url": "https://api.deepseek.com",
  "model": "deepseek-v4-pro"
}
```

`settings.json` contains your key, is excluded by `.gitignore`, and must **not** be committed. Full reference:

| Field | Description | Default |
| --- | --- | --- |
| `api_key` | LLM service key (required) | — |
| `base_url` | OpenAI-compatible endpoint | `https://api.deepseek.com` |
| `api_path` | Path override (empty = auto-append `/chat/completions`) | empty |
| `model` | Model name | `deepseek-v4-pro` |
| `max_tokens` / `max_completion_tokens` | Output cap | unlimited |
| `reasoning_effort` | Reasoning effort (`low`/`medium`/`high`/`max`) | `max` |
| `thinking_enabled` | Chain-of-thought toggle (mapped to thinking on Gemini) | `true` |
| `material_dirs` | Material library scan folders (multiple allowed) | empty |
| `transport_resume_on_pause` | Return playhead to play-start position on pause | `false` |
| `audio.engine_enabled` | Enable the JUCE engine | `true` |
| `audio.driver` / `audio.device` | Audio driver / output device (ASIO etc.) | auto |
| `audio.sample_rate` / `audio.buffer_size` | Sample rate / buffer (engine latency estimate) | 48000 / 256 |
| `audio.backend` | `auto` (native first) or `webaudio` (force browser) | `auto` |

## Development

```bat
wails dev                     : dev mode (hot reload)
wails build                   : production build -> build\bin\AI_MIDI.exe
AI_MIDI.exe -browser          : browser mode (local HTTP on 127.0.0.1:7860)
set AIMIDI_FRONTEND_DIR=frontend\js\.. && AI_MIDI.exe   : serve frontend from disk (hot-swap debugging)
go test ./...                 : full unit / E2E test suite
go run tests/run_e2e.go       : REST API smoke test (start -browser server first)
```

### Layout

```
frontend/          Frontend (Vanilla JS: app/chat/arrangement/pianoroll/engine modules)
internal/          Go domains (chat / llm / midi / mcp / project / server / engine / config / app / tasks)
tests/             E2E (audio pipeline / frontend static assertions / REST smoke)
tools/             Build, probe and dev scripts
docs/              Design & decision documents (Chinese)
```

### Documents

- Audio engine selection & architecture — JUCE decision and process architecture
- JUCE audio engine development plan — milestones and binary distribution compliance
- Engine IPC protocol — main process ↔ engine named-pipe protocol v1
- Arrangement window design — Playlist-style arrangement window baseline
- DAW project export plan — exporting FL / Studio One projects
- JUCE 8 license snapshot — basis for engine binary distribution

## Testing

```bat
go test ./...
```

Covers: MIDI parsing/tail handling, chat pipeline, config validation, static assets & origin guard, audio API, splash, and key frontend static assertions (`tests/frontend_e2e_test.go`).

## License

- The code in this repository is provided under [Apache-2.0](LICENSE).
- `aimidi-engine.exe` is a closed-source component built with JUCE 8, distributed separately as a Release asset and not included in this repository; its use is governed by the JUCE license terms (snapshot in docs/licenses/).
- AI-generated content and third-party soundfonts (SF2) remain the property of their respective rights holders; use them within the terms of your license.

Copyright 2026 AI_MIDI contributors
