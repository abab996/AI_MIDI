cd /d "%~dp0..\engine"
cmake --build build --config Release --target aimidi-engine || exit /b 1
copy /y "build\aimidi-engine_artefacts\Release\aimidi-engine.exe" "..\bin\aimidi-engine.exe" >nul || exit /b 1
echo ENGINE-REBUILT-OK
