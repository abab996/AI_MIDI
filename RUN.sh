#!/usr/bin/env bash
# AI_MIDI Linux 启动脚本
# 桌面模式需要 webkit2gtk（安装依赖见 README）；缺失或远程会话时用:
#   ./AI_MIDI -browser
cd "$(dirname "$0")" || exit 1
if [ ! -x ./AI_MIDI ]; then
  echo "[AI_MIDI] 未找到可执行文件 ./AI_MIDI" >&2
  exit 1
fi
exec ./AI_MIDI "$@"
