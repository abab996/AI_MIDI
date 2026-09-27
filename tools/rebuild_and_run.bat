@echo off
chcp 65001 >nul
cd /d "%~dp0.."

echo ┌────────────────────────────────────────────────────────┐
echo │  [MelodyMate] 正在执行全量重建 (M1-M5 完整原生引擎)       │
echo └────────────────────────────────────────────────────────┘

echo [1/3] 清理旧进程...
taskkill /F /IM melodymate-engine.exe 2>nul
taskkill /F /IM MelodyMate.exe 2>nul

echo [2/3] 编译 JUCE 原生引擎 (ASIO + MixerGraph + PluginHost)...
cd engine
cmake --build build --config Release --target melodymate-engine || (echo 引擎编译失败 & pause & exit /b 1)
copy /y "build\melodymate-engine_artefacts\Release\melodymate-engine.exe" "..\bin\melodymate-engine.exe" >nul || (echo 复制引擎失败 & pause & exit /b 1)
cd ..

echo [3/3] 打包 Wails 桌面主程序...
call wails build || (echo 主程序打包失败 & pause & exit /b 1)
copy /y "build\bin\MelodyMate.exe" "MelodyMate.exe" >nul || (echo 复制主程序失败 & pause & exit /b 1)

echo ========================================================
echo  构建成功！即将启动 MelodyMate...
echo ========================================================
start "" "%~dp0..\MelodyMate.exe" %*
