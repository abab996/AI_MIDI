# 🎹 AI_MIDI — AI-Assisted Music Theory & MIDI Composition

> **English** | [**中文**](README_zh.md)

**AI_MIDI** is a local-first desktop app that lets AI assist your music creation: add chords to a melody, translate lyrics, design vocal runs, generate new ideas — and arrange everything in an FL Studio-style arrangement view. All you need is one API key. Your projects, files and conversations never leave your computer.

> v3.0.0 upgrades AI_MIDI from a web app to a **standalone desktop application** — install and go, no Python required.

---

## ✨ Features

### 💬 Multi-turn Chat (main workbench)
- **Project archive + workbench** dual view — manage your creations like folders
- **The AI operates MIDI directly**: say *"Write an 8-bar C major melody with a syncopated rhythm"* and it reads the built-in theory library, creates the file and organizes your project — every step visible and reversible
- **Workspace binding** — link a local folder; files sync both ways
- **Undo / recycle bin** — every file operation can be rolled back
- **Thinking process visualization** — reasoning and tool calls stream live
- Drafts auto-save per project; parchment / dark-blue dual theme

### ⚡ Quick Tasks (one-shot processing)
- **Add Chords** — harmonize a melody into MIDI
- **Translate Lyrics** — make translations fit the melody (Japanese kana supported)
- **Design Melisma** — generate vocal ornaments & coloratura
- **Custom Requests** — free-form tasks, optionally output as MIDI

### 🎚 Arrangement View (new in v3 · early beta)
- FL Studio-style timeline: tracks, clip dragging, snapping, split, loop range
- Double-click any clip to edit notes in the piano roll
- **Output rack** — swap instrument sounds, import SF2 soundfonts, drag-to-assign
- **Global BPM** — change it once, it applies everywhere; new AI-generated MIDI follows it automatically (existing files keep their own tempo)
- Export to **WAV** with full tail decay
- Auto-save keeps your arrangement safe

> ℹ️ Early beta: VST plugins and AI-link are not supported yet — feedback welcome.

### 🔊 Audio Engine (Windows)
- Built-in JUCE native engine: low-latency ASIO monitoring, live level meters, offline WAV bounce
- Without the engine the app falls back to browser audio automatically (fully functional, slightly higher latency)

---

## 🚀 Quick Start

### Windows (recommended)
1. Grab the latest `AI_MIDI_Setup_<version>_windows_amd64.exe` from [Releases](../../releases) and run it — no admin rights needed
2. On first launch, read the usage notice, then open **Settings** and enter your API key
3. Create a project and start composing!

### Linux
1. Download the latest `AI_MIDI_v<version>_linux_amd64.tar.gz` from [Releases](../../releases) and unzip it
2. `chmod +x RUN.sh && ./RUN.sh` (desktop mode needs webkit2gtk; otherwise `./AI_MIDI -browser` opens in your browser)
3. Linux uses browser audio synthesis — no engine needed

### First-time Configuration
Open the **Settings** page and fill in your API key. The default connects to DeepSeek; any OpenAI-compatible provider works (OpenAI, Gemini, local LLMs…).

| Field | Description |
|---|---|
| **API Key** | Your provider key (stored only in local `settings.json`, never uploaded) |
| **Base URL** | Default `https://api.deepseek.com`; any OpenAI-compatible endpoint works |
| **Model** | Default `deepseek-v4-pro`; click "Refresh" to fetch the list from your provider |

---

## 🛠 Build from Source (developers)

Requirements: **Go ≥ 1.27**, **Wails v2** (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`), Windows 10+ (Linux: webkit2gtk for desktop mode).

```bat
git clone https://github.com/abab996/AI_MIDI.git
cd AI_MIDI
wails build        :: Windows -> buildin\AI_MIDI.exe
RUN.bat
```

Linux: `go build -o AI_MIDI . && chmod +x RUN.sh && ./RUN.sh`. Architecture & design documents live in [`docs/`](docs/).

---

## 📖 Usage Guide

### Quick Tasks
1. On the chat page, click **⚡ Quick Task** at the top
2. Drop in a MIDI file → pick a task → fill in your requirements → Start
3. Download the result when done

### Multi-turn Chat
1. Create a project in the archive (**＋ New Project**)
2. Describe your goal — e.g. *"Harmonize this melody with jazz chords"*
3. The AI reads the theory library first, then creates files step by step; undo anything you don't like

### Arrangement View
1. Click **▦ Arrange** on the workbench
2. Drag MIDI files from the left panel onto tracks, swap sounds from the output rack
3. Set the global BPM, arrange your clips, hit **⬇ WAV** to export

### Tips
- Switch theme (parchment ⇄ dark blue) via the top-right button
- Switching projects auto-saves your draft; it is restored when you return
- Arrangement data auto-saves — still keep backups of important projects

---

## 🔒 Privacy & Security

- **Your API key** is stored only in local `settings.json` — never in code, never uploaded
- **All project data** (conversations, MIDI files) lives on your machine
- The key is sent only to the service address you configure
- The app runs fully locally and collects no usage data

---

## 📄 License

Code licensed under [Apache License 2.0](LICENSE).
The bundled audio engine (`aimidi-engine.exe`, Windows installer) is a closed-source component distributed via the installer.

Developers: architecture and design documents live in [`docs/`](docs/).

---

**AI_MIDI** — compose smarter, theory-first. 🎵
