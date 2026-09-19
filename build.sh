#!/usr/bin/env bash
# 一键构建桌面客户端：bash build.sh            当前平台
#                     bash build.sh windows    交叉编译 Windows 包（需装好 wails CLI）
set -euo pipefail
cd "$(dirname "$0")"
mode="${1:-desktop}"
mkdir -p dist

case "$mode" in
  windows) wails build -platform windows/amd64 -nsis ;;
  mac|darwin) wails build -platform darwin/universal ;;
  *) wails build ;;
esac
echo "桌面包在 build/bin/"
