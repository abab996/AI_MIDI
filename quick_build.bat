@echo off
cd /d "%~dp0"
echo [1] build...
call wails build || exit /b 1
copy /y build\bin\AI_MIDI.exe AI_MIDI.exe >nul
echo [2] verify...
echo ALL-DONE
