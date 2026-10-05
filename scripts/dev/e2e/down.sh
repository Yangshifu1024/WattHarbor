#!/usr/bin/env bash
# 停止隔离栈端到端验收的五个服务进程与模拟器，并关闭 compose.test.yaml 测试实例。
# 只用普通 down 保留数据卷，便于核对滞留事实；需要完全重建时由维护者显式处理。
# 退出后核对 8080-8085、9100、9190、3307、6381 无残留监听。
set -u

# taskkill 按映像名结束进程，仅作用于本验收启动的进程名，不触及容器内服务。
for proc in simulator-dc589 worker central gateway user admin; do
  taskkill //F //IM "$proc.exe" > /dev/null 2>&1 && echo "stopped $proc" || true
done

cd "$(dirname "$0")/../../.." || exit 1
docker compose -f compose.test.yaml down
