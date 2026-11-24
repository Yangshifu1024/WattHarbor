// 恢复 backup.mjs 生成的备份；先删除并重建目标库，避免混入旧数据。
// 除非 RESTORE_CONFIRM 等于归档声明的库名清单，否则拒绝执行。
// gzip 解压由 node:zlib 提供，不依赖外部 gzip 命令。
//
// 用法：
//   RESTORE_CONFIRM="gateway_db central_db worker_db" node scripts/db/restore.mjs backups/wattharbor-20260929T000000Z.sql.gz
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
  const container = process.env.MYSQL_CLIENT_CONTAINER;
  if (container) {
    return { cmd: 'docker', prefix: ['exec', '-i', '-e', 'MYSQL_PWD', container, 'mysql', '-h127.0.0.1', '-u' + user] };
  }
  if (spawnSync('mysql', ['--version'], { stdio: 'ignore' }).status !== 0) {
    fail('no mysql client available; set MYSQL_CLIENT_CONTAINER to the database container');
  }
  return { cmd: 'mysql', prefix: [`-h${host}`, `-P${port}`, `-u${user}`] };
}

function runSql(sqlText) {
  const { cmd, prefix } = resolveCli();
  return spawnSync(cmd, [...prefix, '-N', '-e', sqlText], { cwd: ROOT, encoding: 'utf8', env: nativeEnv });
}

// 从归档头部读取 schemas，确定要替换的数据库；找到声明后停止解压。
function declaredSchemas() {
  return new Promise((resolve, reject) => {
    const lines = createInterface({ input: createReadStream(archive).pipe(createGunzip()), crlfDelay: Infinity });
    lines.on('line', line => {
      if (line.startsWith('-- schemas: ')) {
        // 先 resolve 结果再关闭流，避免 close 事件先用空字符串完成 Promise。
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
const layout = schemas.join(' ');
if (layout !== 'wattharbor' && layout !== 'gateway_db central_db worker_db') {
  fail('unsupported backup schema layout');
}
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
  await pumped;
  if (code !== 0) fail('restore failed while applying the archive; the schemas are partially loaded');
}

// 归档包含 Goose 历史。按归档布局选择版本重放入口。
const unified = layout === 'wattharbor';
const hasGo = spawnSync('go', ['version'], { stdio: 'ignore' }).status === 0;
const hasURLs = unified ? !!process.env.DATABASE_URL
  : ['CENTRAL', 'GATEWAY', 'WORKER'].every(name => process.env[`DATABASE_URL_${name}`]);
if (hasGo && hasURLs) {
  console.error('re-applying migrations after restore');
  const migrated = spawnSync('go', ['run', './cmd/migrate', '--schema', unified ? 'unified' : 'all'], {
    cwd: join(ROOT, 'backend'), stdio: 'inherit',
  });
  if (migrated.status !== 0) fail('migration replay failed after restore');
} else {
  console.error('skipping migration replay: run the deployment migration command before restarting the app');
}

for (const schema of schemas) {
  const counted = runSql(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='${schema}'`);
  console.log(`restored ${schema}: ${counted.stdout.trim()} tables`);
}

// 恢复后提示重启服务，使配置和管理员初始化流程重新执行。
console.log('');
console.log('restart the app after checking the restored data and migration version:');
console.log(`  docker compose -f docker/compose.${unified ? 'prod' : 'dev'}.yaml restart app`);
