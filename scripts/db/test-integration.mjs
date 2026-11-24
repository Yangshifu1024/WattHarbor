// 对开发环境（docker/compose.dev.yaml 的 3306 MySQL 与 6379 Redis）执行三库
// 迁移、真库及会话集成测试。2026-10-06 起不再使用独立 3307/6381 测试实例，
// 测试直连开发环境；注意集成测试的显式主键会抬高开发库自增序号。
// 无参数，Docker／迁移／测试失败即终止。子进程环境只在本次进程内注入，
// 不影响调用方 shell。迁移与测试不在 Docker 内编译，需要 Go 1.27；
// 测试命令统一走仓库根 tasks.mjs test-integration，不依赖 Make。
// 用法：node scripts/db/test-integration.mjs
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');
// Go 模块位于 backend/，迁移与测试命令在该目录执行；compose 编排位于 docker/。
const BACKEND = join(ROOT, 'backend');
const COMPOSE = ['compose', '-f', 'docker/compose.dev.yaml'];

function fail(message) {
  console.error(message);
  process.exit(1);
}

function run(cmd, args, cwd = ROOT) {
  const result = spawnSync(cmd, args, { cwd, stdio: 'inherit' });
  if (result.error?.code === 'ENOENT') return { missing: true };
  return result;
}

const up = run('docker', [...COMPOSE, 'up', '-d', '--wait', 'mysql', 'redis-cache']);
if (up.missing || up.status !== 0) fail('Unable to start development MySQL and Redis.');

// 既存数据卷不会再次执行 entrypoint SQL，故迁移前补幂等的最小只读锁观测授权。
const grant = spawnSync('docker', [...COMPOSE, 'exec', '-T', 'mysql',
  'sh', '-c', 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --protocol=TCP -h 127.0.0.1 -uroot'],
  { cwd: ROOT, input: readFileSync(join(ROOT, 'docker/mysql/test-lock-observer.sql')), stdio: ['pipe', 'inherit', 'inherit'] });
if (grant.status !== 0) fail('Development lock observation grant failed.');

process.env.TEST_MYSQL_URL = 'mysql://wattharbor:wattharbor_dev@127.0.0.1:3306/central_db';
process.env.TEST_REDIS_URL = 'redis://127.0.0.1:6379/0';
process.env.DATABASE_URL_CENTRAL = process.env.TEST_MYSQL_URL;
process.env.DATABASE_URL_GATEWAY = 'mysql://wattharbor:wattharbor_dev@127.0.0.1:3306/gateway_db';
process.env.DATABASE_URL_WORKER = 'mysql://wattharbor:wattharbor_dev@127.0.0.1:3306/worker_db';

const migrated = run('go', ['run', './cmd/migrate', '--schema', 'all'], BACKEND);
if (migrated.missing || migrated.status !== 0) fail('Development migration failed.');

// tasks.mjs 保留「缺 TEST_MYSQL_URL 即失败」的闸门；连接串只在本次进程内注入。
const tested = run(process.execPath, [join(ROOT, 'tasks.mjs'), 'test-integration']);
if (tested.missing || tested.status !== 0) fail('Integration tests failed.');
