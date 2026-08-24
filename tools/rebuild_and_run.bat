@echo off
chcp 65001 >nul
cd /d "%~dp0.."

echo ┌────────────────────────────────────────────────────────┐
echo │  [AI_MIDI] 正在执行全量重建 (M1-M5 完整原生引擎)       │
echo └────────────────────────────────────────────────────────┘

echo [1/3] 清理旧进程...
taskkill /F /IM aimidi-engine.exe 2>nul
taskkill /F /IM AI_MIDI.exe 2>nul

echo [2/3] 编译 JUCE 原生引擎 (ASIO + MixerGraph + PluginHost)...
cd engine
cmake --build build --config Release --target aimidi-engine || (echo 引擎编译失败 & pause & exit /b 1)
copy /y "build\aimidi-engine_artefacts\Release\aimidi-engine.exe" "..\bin\aimidi-engine.exe" >nul || (echo 复制引擎失败 & pause & exit /b 1)
cd ..

echo [3/3] 打包 Wails 桌面主程序...
call wails build || (echo 主程序打包失败 & pause & exit /b 1)
copy /y "build\bin\AI_MIDI.exe" "AI_MIDI.exe" >nul || (echo 复制主程序失败 & pause & exit /b 1)

echo ========================================================
echo  构建成功！即将启动 AI_MIDI...
echo ========================================================
start "" "%~dp0..\AI_MIDI.exe" %*
