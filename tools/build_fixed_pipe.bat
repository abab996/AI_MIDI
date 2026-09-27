cd /d "%~dp0..\engine"
cmake --build build --config Release --target melodymate-engine || exit /b 1
copy /y "build\melodymate-engine_artefacts\Release\melodymate-engine.exe" "..\bin\melodymate-engine.exe" >nul || exit /b 1
echo ENGINE-REBUILT-OK
