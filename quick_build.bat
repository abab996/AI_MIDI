@echo off
cd /d "%~dp0"
echo [1] build...
call wails build || exit /b 1
copy /y build\bin\AI_MIDI.exe AI_MIDI.exe >nul || (
    echo [错误] AI_MIDI.exe 复制失败：文件被占用（应用可能正在运行），请先关闭应用再重新构建
    exit /b 1
)
echo [2] verify...
echo ALL-DONE
