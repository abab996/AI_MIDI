#!/usr/bin/env bash
# MelodyMate Linux 启动脚本
# 桌面模式需要 webkit2gtk（安装依赖见 README）；缺失或远程会话时用:
#   ./MelodyMate -browser
cd "$(dirname "$0")" || exit 1
if [ ! -x ./MelodyMate ]; then
  echo "[MelodyMate] 未找到可执行文件 ./MelodyMate" >&2
  exit 1
fi
exec ./MelodyMate "$@"
