@echo off
chcp 65001 >nul
cd /d "%~dp0.."

echo [1/3] 终止旧进程...
taskkill /F /IM aimidi-engine.exe 2>nul
taskkill /F /IM AI_MIDI.exe 2>nul

echo [2/3] 编译引擎 (固定管道名)...
cd engine
cmake --build build --config Release --target aimidi-engine || (echo 引擎编译失败 & exit /b 1)
copy /y "build\aimidi-engine_artefacts\Release\aimidi-engine.exe" "..\bin\aimidi-engine.exe" >nul || (echo 引擎复制失败 & exit /b 1)
cd ..

echo [3/3] 打包 Wails 主程序...
call wails build || (echo Wails 打包失败 & exit /b 1)
copy /y "build\bin\AI_MIDI.exe" "AI_MIDI.exe" >nul || (echo 主程序复制失败 & exit /b 1)

echo [4/4] 组装 build\bin 自测布局（引擎/知识库/音色）...
call tools\assemble_buildbin.bat || (echo 布局组装失败 & exit /b 1)

echo ========================================================
echo  全部构建完成！固定管道名已生效，彻底杜绝 PID 猜测死锁。
echo  请直接运行 RUN.bat
echo ========================================================
