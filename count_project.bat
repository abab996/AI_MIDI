@echo off
setlocal
title AI_MIDI-go Project Stats
cd /d "%~dp0"

set "EXE=tools\projstats\projstats.exe"

if exist "%EXE%" goto run

echo [初始化] 首次运行，正在编译 Go 统计工具 ...
where go >nul 2>nul
if errorlevel 1 (
    echo [错误] 未找到 projstats.exe，且系统未安装 Go 工具链。
    echo        请先安装 Go，或手动编译: cd tools\projstats ^&^& go build -o projstats.exe .
    pause
    exit /b 1
)
pushd tools\projstats
go build -ldflags "-s -w" -o projstats.exe .
if errorlevel 1 (
    popd
    echo [错误] 编译失败，请检查 Go 环境。
    pause
    exit /b 1
)
popd
echo [完成] 编译成功。

:run
"%EXE%"
echo.
pause
endlocal
