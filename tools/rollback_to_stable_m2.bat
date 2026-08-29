@echo off
chcp 65001 >nul
cd /d "%~dp0.."

echo ========================================================
echo  正在回滚到今天最稳定的 M2 交付基线...
echo  主仓基线: b5b4afe (M2 阶段五: 卷帘SF2弹奏 + 设置页面板)
echo  引擎基线: 701b916 (M2 阶段一: tinySoundFont 合成器)
echo ========================================================
echo  警告：主仓与 engine 子仓将 git reset --hard，
echo  所有未提交的改动会永久丢失且不可恢复！
choice /c YN /m "确认回滚"
if errorlevel 2 (
    echo 已取消。
    exit /b 1
)

echo [1/5] 终止残留进程...
taskkill /F /IM aimidi-engine.exe 2>nul
taskkill /F /IM AI_MIDI.exe 2>nul

echo [2/5] 回滚主仓库代码...
git reset --hard b5b4afe || exit /b 1

echo [3/5] 回滚引擎仓库代码...
cd engine
git reset --hard 701b916 || exit /b 1

echo [4/5] 重新编译引擎 (MSVC Release)...
cmake --build build --config Release --target aimidi-engine || exit /b 1
copy /y "build\aimidi-engine_artefacts\Release\aimidi-engine.exe" "..\bin\aimidi-engine.exe" >nul || exit /b 1
cd ..

echo [5/5] 重新打包 Wails 主程序...
call wails build || exit /b 1
copy /y "build\bin\AI_MIDI.exe" "AI_MIDI.exe" >nul || exit /b 1

echo ========================================================
echo  回滚并重新编译完成！
echo  已恢复到完全可用的 M2 稳定基线。
echo  请直接运行 RUN.bat
echo ========================================================
