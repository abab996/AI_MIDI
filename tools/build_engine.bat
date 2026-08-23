@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0..\engine"

echo [build_engine] 检查 JUCE submodule...
if not exist "ThirdParty\JUCE\CMakeLists.txt" (
    echo [错误] ThirdParty\JUCE 缺失。请先执行：
    echo     cd engine ^&^& git submodule update --init --recursive
    exit /b 1
)

echo [build_engine] CMake 配置（VS2022 x64）...
cmake -S . -B build -G "Visual Studio 17 2022" -A x64 || exit /b 1

echo [build_engine] 编译 Release x64...
cmake --build build --config Release --target aimidi-engine || exit /b 1

if not exist "..\bin" mkdir "..\bin"
copy /y "build\aimidi-engine_artefacts\Release\aimidi-engine.exe" "..\bin\aimidi-engine.exe" >nul || (
    echo [错误] 复制产物失败，请检查 build\aimidi-engine_artefacts\Release\
    exit /b 1
)

echo [OK] 已更新 bin\aimidi-engine.exe
endlocal
