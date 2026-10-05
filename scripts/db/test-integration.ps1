# 在独立 MySQL／Redis 中执行三库迁移、真库及会话测试；不连接或重建开发实例。
# 无参数，Docker／迁移／测试失败即终止；退出时恢复调用方的数据库与 Redis 环境变量。
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path "$PSScriptRoot/../..").Path
$keys = @('TEST_MYSQL_URL', 'TEST_REDIS_URL', 'DATABASE_URL_CENTRAL', 'DATABASE_URL_GATEWAY', 'DATABASE_URL_WORKER')
$saved = @{}
foreach ($key in $keys) { $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
Push-Location $root
try {
  docker compose -f compose.test.yaml up -d --wait mysql redis
  if ($LASTEXITCODE -ne 0) { throw 'Unable to start isolated integration MySQL and Redis.' }
  $env:TEST_MYSQL_URL = 'mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/central_db'
  $env:TEST_REDIS_URL = 'redis://127.0.0.1:6381/0'
  $env:DATABASE_URL_CENTRAL = $env:TEST_MYSQL_URL
  $env:DATABASE_URL_GATEWAY = 'mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/gateway_db'
  $env:DATABASE_URL_WORKER = 'mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/worker_db'
  go run ./cmd/migrate --schema all
  if ($LASTEXITCODE -ne 0) { throw 'Isolated integration migration failed.' }
  make test-integration
  if ($LASTEXITCODE -ne 0) { throw 'Isolated integration tests failed.' }
} finally {
  foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
  Pop-Location
}
