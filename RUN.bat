@echo off
chcp 65001 >nul
title MelodyMate · AI 编曲助手
cd /d "%~dp0"

echo ┌────────────────────────────────────────────────────────┐
echo │  [MelodyMate] 工程蓝图级 AI 编曲助手 (Go + Wails)         │
echo │  正在启动桌面客户端...                                │
echo └────────────────────────────────────────────────────────┘

if not exist "MelodyMate.exe" (
    echo [提示] 未找到预编译可执行文件，正在执行 Wails 构建...
    call wails build
    if errorlevel 1 (
        echo [错误] Wails 构建失败，请检查上方错误信息后重试。
        pause
        exit /b 1
    )
    if not exist "build\bin\MelodyMate.exe" (
        echo [错误] 构建未产出 build\bin\MelodyMate.exe，无法继续。
        pause
        exit /b 1
    )
    copy /y "build\bin\MelodyMate.exe" "MelodyMate.exe" >nul
)

if not exist "bin\melodymate-engine.exe" (
    echo [提示] 未找到原生音频引擎 bin\melodymate-engine.exe，将以浏览器音频模式运行（延迟较高）。
    echo        如需启用原生引擎，请先运行 tools\build_engine.bat。
)

start "" "%~dp0MelodyMate.exe" %*
