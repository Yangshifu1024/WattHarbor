// 备份并重建三个本地开发 schema，随后清空本项目专属的两套 Redis 并重启开发栈。
// 破坏性动作：只允许对明确可丢弃的开发数据执行；必须显式传入
// --reset-development-data，防止误清开发库。不提供交互确认。
// 用法：node scripts/db/reset-dev.mjs --reset-development-data
import { spawnSync } from 'node:child_process';
import { statSync, mkdirSync, copyFileSync, rmSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { parseArgs } from 'node:util';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');
const COMPOSE = ['compose', '-f', 'compose.dev.yaml'];
const CONTAINER = 'chargepilot-dev-mysql-1';
const REDIS_CONTAINERS = ['chargepilot-dev-redis-cache-1', 'chargepilot-dev-redis-stream-1'];

function fail(message) {
  console.error(message);
  process.exit(1);
}

const { values } = parseArgs({ options: { 'reset-development-data': { type: 'boolean' } } });
if (!values['reset-development-data']) {
  fail('Pass --reset-development-data to back up and rebuild the three local development schemas.');
}

function run(cmd, args) {
  const result = spawnSync(cmd, args, { cwd: ROOT, stdio: 'inherit' });
  if (result.error?.code === 'ENOENT') return { missing: true };
  return result;
}

// 只认本仓库开发项目的 MySQL 容器，避免对同名他项容器执行毁灭性 SQL。
const inspected = spawnSync('docker', ['inspect', CONTAINER, '--format',
  '{{ index .Config.Labels "com.docker.compose.project" }}'], { cwd: ROOT, encoding: 'utf8' });
if (inspected.status !== 0 || inspected.stdout.trim() !== 'chargepilot-dev') {
  fail('Expected local chargepilot-dev MySQL container.');
}

const backupDir = join(ROOT, '.tmp');
mkdirSync(backupDir, { recursive: true });
const backup = join(backupDir, `dev-db-before-reset-${new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z')}.sql`);

const stopped = run('docker', [...COMPOSE, 'stop', 'user', 'admin', 'central', 'gateway', 'worker']);
if (stopped.status !== 0) fail('Unable to stop backend services.');

// 备份先于一切破坏性动作落盘；容器内重定向需要 sh，宿主机侧经 docker cp 取回。
const dumped = spawnSync('docker', ['exec', CONTAINER, 'sh', '-c',
  'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -u root --single-transaction --routines --triggers --databases gateway_db central_db worker_db > /tmp/chargepilot-before-reset.sql'],
  { cwd: ROOT, stdio: 'inherit' });
if (dumped.status !== 0) fail('Backup failed; schemas were not reset.');

const copied = spawnSync('docker', ['cp', `${CONTAINER}:/tmp/chargepilot-before-reset.sql`, backup], { cwd: ROOT, stdio: 'inherit' });
if (copied.status !== 0 || statSync(backup).size === 0) fail('Backup copy failed; schemas were not reset.');
rmSync(join(backupDir, 'chargepilot-before-reset.sql'), { force: true });

// DROP 后按初始化同款字符集重建；此时备份已在 .tmp/，失败也留有恢复依据。
const sql = [
  'DROP DATABASE gateway_db; CREATE DATABASE gateway_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;',
  'DROP DATABASE central_db; CREATE DATABASE central_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;',
  'DROP DATABASE worker_db; CREATE DATABASE worker_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;',
].join(' ');
const reset = spawnSync('docker', ['exec', '-i', CONTAINER, 'sh', '-c', 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -u root'],
  { cwd: ROOT, input: sql, stdio: ['pipe', 'inherit', 'inherit'] });
if (reset.status !== 0) fail('Reset failed; backup exists, keep services stopped.');

// 这两套 Redis 是本项目专属：旧会话与任务引用着随 schema 一起丢弃的业务行。
for (const redis of REDIS_CONTAINERS) {
  const flushed = spawnSync('docker', ['exec', redis, 'redis-cli', 'FLUSHDB'], { cwd: ROOT, stdio: 'inherit' });
  if (flushed.status !== 0) fail(`Unable to reset project Redis: ${redis}`);
}

const migrated = run('docker', [...COMPOSE, 'run', '--rm', 'migrate']);
if (migrated.status !== 0) fail('Init failed; keep services stopped and inspect the backup.');

const restarted = run('docker', [...COMPOSE, 'up', '-d', 'user', 'admin', 'central', 'gateway', 'worker', 'admin-web']);
if (restarted.status !== 0) fail('Unable to restart the development stack.');

console.log(`Development schemas rebuilt. Backup: ${backup}`);
