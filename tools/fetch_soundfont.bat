@echo off
rem ============================================================
rem Fetch the bundled default soundfont GeneralUser GS v1.471
rem (~27 MB) into Library\soundfonts. Redistribution is allowed
rem by its license (see LICENSE.txt, author S. Christian Collins).
rem MUST run before ISCC packaging: AI_MIDI.iss requires the file.
rem ============================================================
setlocal
cd /d "%~dp0.."

set "SFDIR=Library\soundfonts"
set "SFNAME=GeneralUser GS v1.471.sf2"
set "SF=%SFDIR%\%SFNAME%"
set "SFURL=https://raw.githubusercontent.com/JustEnoughLinuxOS/generaluser-gs/main/GeneralUser%%20GS%%20v1.471.sf2"
set "LICURL=https://raw.githubusercontent.com/JustEnoughLinuxOS/generaluser-gs/main/LICENSE.txt"

if exist "%SF%" (
    echo [fetch_soundfont] already exists, skip download: %SF%
    goto license
)
if not exist "%SFDIR%" mkdir "%SFDIR%"
echo [fetch_soundfont] downloading GeneralUser GS v1.471 (~27 MB)...
curl -fL --retry 3 --progress-bar -o "%SF%" "%SFURL%"
if errorlevel 1 (
    echo [ERROR] download failed. Check network and retry, or download manually to:
    echo         %SF%
    echo source: https://github.com/JustEnoughLinuxOS/generaluser-gs
    exit /b 1
)

:license
if not exist "%SFDIR%\GeneralUser GS LICENSE.txt" (
    curl -fL --retry 3 -s -o "%SFDIR%\GeneralUser GS LICENSE.txt" "%LICURL%"
    if errorlevel 1 echo [WARN] license download failed
)
if not exist "%SFDIR%\GeneralUser GS LICENSE.txt" (
    echo [ERROR] missing license file "%SFDIR%\GeneralUser GS LICENSE.txt" (required for redistribution)
    exit /b 1
)

echo [OK] default soundfont ready: %SF%
endlocal
