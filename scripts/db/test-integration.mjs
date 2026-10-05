// 在独立 MySQL／Redis 中执行三库迁移、真库及会话测试；不连接或重建开发实例。
// 无参数，Docker／迁移／测试失败即终止。子进程环境只在本次进程内注入，
// 不影响调用方 shell。迁移与测试不在 Docker 内编译（与原入口一致），
// 需要 Go 1.27；测试命令优先走 make test-integration，缺少 make 时
// 直接执行等价的 go test，保证跨平台可用。
// 用法：node scripts/db/test-integration.mjs
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');
const COMPOSE = ['compose', '-f', 'compose.test.yaml'];

function fail(message) {
  console.error(message);
  process.exit(1);
}

function run(cmd, args) {
  const result = spawnSync(cmd, args, { cwd: ROOT, stdio: 'inherit' });
  if (result.error?.code === 'ENOENT') return { missing: true };
  return result;
}

const up = run('docker', [...COMPOSE, 'up', '-d', '--wait', 'mysql', 'redis']);
if (up.missing || up.status !== 0) fail('Unable to start isolated integration MySQL and Redis.');

// 既存测试卷不会再次执行 entrypoint SQL，故迁移前补幂等的最小只读锁观测授权。
const grant = spawnSync('docker', [...COMPOSE, 'exec', '-T', 'mysql',
  'sh', '-c', 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --protocol=TCP -h 127.0.0.1 -uroot'],
  { cwd: ROOT, input: readFileSync(join(ROOT, 'docker/mysql/test-lock-observer.sql')), stdio: ['pipe', 'inherit', 'inherit'] });
if (grant.status !== 0) fail('Isolated integration lock observation grant failed.');

process.env.TEST_MYSQL_URL = 'mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/central_db';
process.env.TEST_REDIS_URL = 'redis://127.0.0.1:6381/0';
process.env.DATABASE_URL_CENTRAL = process.env.TEST_MYSQL_URL;
process.env.DATABASE_URL_GATEWAY = 'mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/gateway_db';
process.env.DATABASE_URL_WORKER = 'mysql://chargepilot:chargepilot_dev@127.0.0.1:3307/worker_db';

const migrated = run('go', ['run', './cmd/migrate', '--schema', 'all']);
if (migrated.missing || migrated.status !== 0) fail('Isolated integration migration failed.');

// make test-integration 自带「缺 TEST_MYSQL_URL 即失败」的闸门；这里已注入
// 连接串，缺 make 的系统直接跑同一条 go test，不提供跳过路径。
const made = run('make', ['test-integration']);
if (made.missing) {
  console.error('make not found; running go test directly');
  const tested = run('go', ['test', '-tags=integration', '-count=1', './...']);
  if (tested.status !== 0) fail('Isolated integration tests failed.');
} else if (made.status !== 0) {
  fail('Isolated integration tests failed.');
}
