// 把 scripts/db/backup.mjs 产出的备份恢复到配置好的各个库。
// 破坏性是刻意设计的：先 DROP 每个库，恢复才不会和陈旧数据悄悄合并。
// 除非 RESTORE_CONFIRM 等于归档声明的库名清单，否则拒绝执行。
// gzip 解压由 node:zlib 提供，不依赖外部 gzip 命令。
//
// 用法：
//   RESTORE_CONFIRM="gateway_db central_db worker_db" node scripts/db/restore.mjs backups/chargepilot-20260929T000000Z.sql.gz
import { spawnSync, spawn } from 'node:child_process';
import { createReadStream } from 'node:fs';
import { createGunzip } from 'node:zlib';
import { pipeline } from 'node:stream/promises';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');
const archive = process.argv[2] ?? '';

function fail(message) {
  console.error(message);
  process.exit(1);
}

if (!archive) {
  console.error('usage: RESTORE_CONFIRM="<schemas>" node scripts/db/restore.mjs <archive.sql.gz>');
  process.exit(2);
}

const host = process.env.MYSQL_HOST ?? '127.0.0.1';
const port = process.env.MYSQL_PORT ?? '3306';
const user = process.env.MYSQL_USER ?? 'root';
const password = process.env.MYSQL_PASSWORD ?? '';
const nativeEnv = { ...process.env };
if (!nativeEnv.MYSQL_PWD && password) nativeEnv.MYSQL_PWD = password;

function resolveCli() {
  if (spawnSync('mysql', ['--version'], { stdio: 'ignore' }).status === 0) {
    return { cmd: 'mysql', prefix: [`-h${host}`, `-P${port}`, `-u${user}`] };
  }
  const container = process.env.MYSQL_CLIENT_CONTAINER;
  if (!container) fail('no mysql client available; set MYSQL_CLIENT_CONTAINER to the database container');
  return { cmd: 'docker', prefix: ['exec', '-i', '-e', `MYSQL_PWD=${password}`, container, 'mysql', '-h127.0.0.1', '-u' + user] };
}

function runSql(sqlText) {
  const { cmd, prefix } = resolveCli();
  return spawnSync(cmd, [...prefix, '-N', '-e', sqlText], { cwd: ROOT, encoding: 'utf8', env: nativeEnv });
}

// 归档才是「要替换掉什么」的准绳，操作者必须确认的正是这几个库。
// 只扫归档头部：找到 schemas 声明行即停止解压。
function declaredSchemas() {
  return new Promise((resolve, reject) => {
    const lines = createInterface({ input: createReadStream(archive).pipe(createGunzip()), crlfDelay: Infinity });
    lines.on('line', line => {
      if (line.startsWith('-- schemas: ')) {
        // 先兑现结果再关流：close 事件会同步触发兜底 resolve('')，
        // 顺序颠倒会让空值抢先成为 Promise 的兑现值。
        const declared = line.slice('-- schemas: '.length).trim();
        resolve(declared);
        lines.close();
      }
    });
    lines.on('close', () => resolve(''));
    lines.on('error', reject);
  });
}

let targets;
try {
  if (!archive.endsWith('.sql.gz')) fail(`archive does not look like a gzip dump: ${archive}`);
  targets = await declaredSchemas();
} catch {
  fail(`archive does not declare which schemas it contains: ${archive}`);
}
if (!targets) fail(`archive does not declare which schemas it contains: ${archive}`);
const schemas = targets.split(/\s+/).filter(Boolean);
if (schemas.join(' ') !== (process.env.RESTORE_CONFIRM ?? '').trim().split(/\s+/).filter(Boolean).join(' ')) {
  console.error('refusing to restore without confirmation.');
  console.error(`archive contains: ${targets}`);
  console.error(`set RESTORE_CONFIRM="${targets}" to proceed (this DROPS those schemas)`);
  process.exit(1);
}

// 只有 DROP 再重建，才能保证恢复后的数据与归档一致，而不是叠在原有数据之上。
for (const schema of schemas) {
  const dropped = runSql(`DROP DATABASE IF EXISTS \`${schema}\`; CREATE DATABASE \`${schema}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;`);
  if (dropped.status !== 0) fail(`unable to drop and recreate ${schema}: ${dropped.stderr}`);
}

{
  const { cmd, prefix } = resolveCli();
  const child = spawn(cmd, prefix, { cwd: ROOT, env: nativeEnv, stdio: ['pipe', 'inherit', 'inherit'] });
  const pumped = pipeline(createReadStream(archive), createGunzip(), child.stdin);
  const code = await new Promise((resolve, reject) => {
    child.on('exit', resolve);
    child.on('error', reject);
  });
  await pumped.catch(() => {});
  if (code !== 0) fail('restore failed while applying the archive; the schemas are partially loaded');
}

// 恢复后必须重跑 Goose：迁移历史本身就存在数据库里，跳过会让迁移没被记录。
// 连接串取自环境变量，所以只在配置齐全时才执行。
const hasGo = spawnSync('go', ['version'], { stdio: 'ignore' }).status === 0;
if (hasGo && process.env.DATABASE_URL_CENTRAL) {
  console.error('re-applying migrations after restore');
  // Go 模块位于 backend/，迁移重放在该目录执行。
  const migrated = spawnSync('go', ['run', './cmd/migrate', '--schema', 'all'], { cwd: join(ROOT, 'backend'), stdio: 'inherit' });
  if (migrated.status !== 0) fail('migration replay failed after restore');
} else {
  console.error('skipping migration replay: set DATABASE_URL_* to re-apply migrations');
}

for (const schema of schemas) {
  const counted = runSql(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='${schema}'`);
  console.log(`restored ${schema}: ${counted.stdout.trim()} tables`);
}

// 各服务在启动时读取配置与引导状态。恢复把管理后台的表清空后，在重启之前
// 没有人能登录，所以这里明确提示，而不是让人撞上无声的锁死。
console.log('');
console.log('restart the services so they reload the restored data:');
console.log('  docker compose restart central gateway worker   # development stack');
console.log('  docker compose -f docker/compose.prod.yaml up -d   # production stack');
