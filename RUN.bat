@echo off
chcp 65001 >nul
title AI_MIDI · AI 编曲助手
cd /d "%~dp0"

echo ┌────────────────────────────────────────────────────────┐
echo │  [AI_MIDI] 工程蓝图级 AI 编曲助手 (Go + Wails)         │
echo │  正在启动桌面客户端...                                │
echo └────────────────────────────────────────────────────────┘

if not exist "AI_MIDI.exe" (
    echo [提示] 未找到预编译可执行文件，正在执行 Wails 构建...
    call wails build
    if exist "build\bin\AI_MIDI.exe" (
        copy /y "build\bin\AI_MIDI.exe" "AI_MIDI.exe" >nul
    )
)

if not exist "bin\aimidi-engine.exe" (
    echo [提示] 未找到原生音频引擎 bin\aimidi-engine.exe，将以浏览器音频模式运行（延迟较高）。
    echo        如需启用原生引擎，请先运行 tools\build_engine.bat。
)

start "" "%~dp0AI_MIDI.exe" %*
