#!/usr/bin/env bash
# 隔离栈端到端验收：以本地二进制启动五个服务进程，全部指向 compose.test.yaml
# 的 3307 测试三库与 6381 测试 Redis，支付走 simulation、登录走 development。
# 前置：compose.test.yaml 已 up --wait 并由 cmd/migrate 应用三库迁移，
# 且已执行 node scripts/dev/prepare.mjs 建立厂商、站点、设备与方案。
# 本脚本一次性验收使用，配合同目录 down.sh 停止，不留常驻服务。
set -u
cd "$(dirname "$0")/../../.." || exit 1
ROOT="$(pwd)"
LOG="$ROOT/.tmp/e2e-logs"
mkdir -p "$LOG" "$ROOT/.tmp/e2e-exports"

DB="mysql://chargepilot:chargepilot_dev@127.0.0.1:3307"
# 服务令牌与 JWT 密钥仅注入本机隔离栈；compose.test.yaml 凭据已是仓库内公开测试值。
ST="e2e-internal-service-token-0123456789abcdef"
JT="e2e-jwt-secret-0123456789abcdef0123456789abcdef"

nohup env DATABASE_URL_CENTRAL="$DB/central_db" REDIS_CACHE_URL="redis://127.0.0.1:6381/0" \
  SERVICE_TOKEN="$ST" CENTRAL_HTTP_ADDR=":8082" GATEWAY_INTERNAL_URL="http://127.0.0.1:8083" \
  WECHAT_APPID="e2e-appid" PAYMENT_MODE="simulation" \
  "$ROOT/bin/central.exe" > "$LOG/central.log" 2>&1 &
echo "central $!"

nohup env DATABASE_URL="$DB/gateway_db" SERVICE_TOKEN="$ST" \
  GATEWAY_HTTP_ADDR=":8083" DC589_ADDR=":9100" CENTRAL_INTERNAL_URL="http://127.0.0.1:8082" \
  "$ROOT/bin/gateway.exe" > "$LOG/gateway.log" 2>&1 &
echo "gateway $!"

nohup env DATABASE_URL_CENTRAL="$DB/central_db" REDIS_CACHE_URL="redis://127.0.0.1:6381/0" \
  JWT_SECRET="$JT" SERVICE_TOKEN="$ST" ADMIN_HTTP_ADDR=":8081" \
  ADMIN_BOOTSTRAP_USER="admin" ADMIN_BOOTSTRAP_PASSWORD="ChangeMe!Admin2026" \
  GATEWAY_INTERNAL_URL="http://127.0.0.1:8083" EXPORT_DIR="$ROOT/.tmp/e2e-exports" \
  "$ROOT/bin/admin.exe" > "$LOG/admin.log" 2>&1 &
echo "admin $!"

nohup env DATABASE_URL_CENTRAL="$DB/central_db" REDIS_CACHE_URL="redis://127.0.0.1:6381/0" \
  JWT_SECRET="$JT" SERVICE_TOKEN="$ST" USER_HTTP_ADDR=":8080" \
  GATEWAY_INTERNAL_URL="http://127.0.0.1:8083" WECHAT_APPID="e2e-appid" WECHAT_SECRET="e2e-app-secret" \
  PAYMENT_MODE="simulation" LOGIN_MODE="development" \
  "$ROOT/bin/user.exe" > "$LOG/user.log" 2>&1 &
echo "user $!"

# worker 同时持有 central 内部端点与 gateway 内部端点两个出站方向，缺一即滞留任务。
nohup env DATABASE_URL_CENTRAL="$DB/central_db" DATABASE_URL_WORKER="$DB/worker_db" \
  DATABASE_URL_GATEWAY="$DB/gateway_db" REDIS_STREAM_URL="redis://127.0.0.1:6381/1" \
  SERVICE_TOKEN="$ST" WORKER_HTTP_ADDR=":8085" \
  CENTRAL_INTERNAL_URL="http://127.0.0.1:8082" GATEWAY_INTERNAL_URL="http://127.0.0.1:8083" \
  "$ROOT/bin/worker.exe" > "$LOG/worker.log" 2>&1 &
echo "worker $!"

# 就绪判定：五进程 ready 全部 200 才视为可用；失败时读 .tmp/e2e-logs 下对应日志。
for port in 8080 8081 8082 8083 8085; do
  ready=""
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if curl -sf -m 2 "http://127.0.0.1:$port/health/ready" > /dev/null 2>&1; then ready=yes; break; fi
    sleep 1
  done
  if [ "$ready" = yes ]; then echo "ready $port"; else echo "NOT-READY $port"; exit 1; fi
done
