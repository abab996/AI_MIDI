@echo off
rem ============================================================
rem Assemble the build\bin self-test layout. A fresh wails build
rem leaves a lone AI_MIDI.exe in build\bin; the engine / knowledge
rem base / soundfont resolution depends on the run directory, so
rem double-clicking the artifact yields "engine missing" (WEB only)
rem or "no soundfont" (native silent). This copies everything next
rem to the exe so build\bin\AI_MIDI.exe can be self-tested directly.
rem ============================================================
setlocal
cd /d "%~dp0.."

if not exist "build\bin" (
    echo [ERROR] build\bin not found - run "wails build" first
    exit /b 1
)

if not exist "build\bin\bin" mkdir "build\bin\bin"
if not exist "bin\aimidi-engine.exe" (
    echo [WARN] bin\aimidi-engine.exe missing - run tools\build_engine.bat first; layout will have no native engine
) else (
    copy /y "bin\aimidi-engine.exe" "build\bin\bin\aimidi-engine.exe" >nul || (echo [ERROR] engine copy failed & exit /b 1)
    echo [OK] engine placed: build\bin\bin\aimidi-engine.exe
)

if not exist "build\bin\Library" mkdir "build\bin\Library"
copy /y "Library\*.md" "build\bin\Library\" >nul 2>&1
echo [OK] knowledge base placed: build\bin\Library\

if exist "Library\soundfonts\GeneralUser GS v1.471.sf2" (
    if not exist "build\bin\Library\soundfonts" mkdir "build\bin\Library\soundfonts"
    copy /y "Library\soundfonts\GeneralUser GS v1.471.sf2" "build\bin\Library\soundfonts\" >nul 2>&1
    copy /y "Library\soundfonts\GeneralUser GS LICENSE.txt" "build\bin\Library\soundfonts\" >nul 2>&1
    echo [OK] default soundfont placed: build\bin\Library\soundfonts\
) else (
    echo [HINT] default soundfont missing - run tools\fetch_soundfont.bat to download
)

echo [OK] build\bin layout assembled; double-click build\bin\AI_MIDI.exe to self-test
