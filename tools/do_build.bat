cd /d "%~dp0.."
call wails build || exit /b 1
copy /y "build\bin\AI_MIDI.exe" "AI_MIDI.exe" >nul || exit /b 1
echo WAILS-BUILD-SUCCESS
