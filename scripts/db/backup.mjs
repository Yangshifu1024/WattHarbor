// 将三个数据库备份到同一个压缩包；完整解压检查失败时退出报错。
// gzip 由 node:zlib 提供，不依赖外部 gzip 命令；MySQL 客户端可用宿主机
// mysql/mysqldump，或经 MYSQL_CLIENT_CONTAINER 在数据库容器内执行。
//
// 用法：
//   node scripts/db/backup.mjs <output-dir>
//   MYSQL_HOST=... MYSQL_PORT=... MYSQL_USER=... MYSQL_PWD=... node scripts/db/backup.mjs out/
//   MYSQL_CLIENT_CONTAINER=wattharbor-dev-mysql-1 MYSQL_PASSWORD=... node scripts/db/backup.mjs out/
import { spawnSync, spawn } from 'node:child_process';
import { createWriteStream, createReadStream, statSync, existsSync } from 'node:fs';
import { createGzip, createGunzip } from 'node:zlib';
import { pipeline, finished } from 'node:stream/promises';
import { Readable } from 'node:stream';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');
const SCHEMAS = ['gateway_db', 'central_db', 'worker_db'];

const outDir = (process.argv[2] ?? './backups').replace(/[\\/]+$/, '');
const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
const host = process.env.MYSQL_HOST ?? '127.0.0.1';
const port = process.env.MYSQL_PORT ?? '3306';
const user = process.env.MYSQL_USER ?? 'root';
const password = process.env.MYSQL_PASSWORD ?? '';

function fail(message) {
  console.error(message);
  process.exit(1);
}

// 原生客户端使用 MYSQL_PWD；仅提供 MYSQL_PASSWORD 时将其映射过去。
const nativeEnv = { ...process.env };
if (!nativeEnv.MYSQL_PWD && password) nativeEnv.MYSQL_PWD = password;

// MySQL 客户端可能只存在于数据库容器里：优先取宿主机 mysql/mysqldump，
// 否则按 MYSQL_CLIENT_CONTAINER 经 docker exec 在容器内连 127.0.0.1。
function resolveCli(tool) {
  if (spawnSync(tool, ['--version'], { stdio: 'ignore' }).status === 0) {
    return { cmd: tool, prefix: [`-h${host}`, `-P${port}`, `-u${user}`] };
  }
  const container = process.env.MYSQL_CLIENT_CONTAINER;
  if (!container) fail(`no ${tool} available; set MYSQL_CLIENT_CONTAINER to the database container`);
  return { cmd: 'docker', prefix: ['exec', '-e', `MYSQL_PWD=${password}`, container, tool, '-h127.0.0.1', '-u' + user] };
}

function runCli(tool, args) {
  const { cmd, prefix } = resolveCli(tool);
  return spawnSync(cmd, [...prefix, ...args], { cwd: ROOT, encoding: 'utf8', env: nativeEnv });
}

// 输出目录必须已存在，由操作者指定备份位置。
if (!existsSync(outDir) || !statSync(outDir).isDirectory()) {
  fail(`create output directory first: ${outDir}`);
}

// 任一数据库不存在时停止备份，避免生成缺库的归档。
for (const schema of SCHEMAS) {
  const probe = runCli('mysql', ['-N', '-e', `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='${schema}'`]);
  if (probe.status !== 0 || probe.stdout.trim() === '') {
    fail(`schema ${schema} does not exist; refusing to produce a partial backup`);
  }
}

const archive = join(outDir, `wattharbor-${stamp}.sql.gz`);
// 禁用 GTID、列统计和表空间输出，兼容容器内外的 MySQL 客户端。
const dumpArgs = ['--single-transaction', '--routines', '--triggers', '--events', '--set-gtid-purged=OFF', '--column-statistics=0', '--no-tablespaces'];

// 保留 gzip 流用于后续写入，pipe() 返回的是目标文件流。
// 多段数据共用压缩流，临时监听器较多，适当提高告警阈值。
const gzip = createGzip({ level: 9 }).setMaxListeners(25);
const sink = createWriteStream(archive);
gzip.pipe(sink);

await pipeline(Readable.from(`-- WattHarbor backup ${stamp}\n-- schemas: ${SCHEMAS.join(' ')}\n`), gzip, { end: false });

for (const schema of SCHEMAS) {
  // 每份数据库 dump 前写入对应的 USE 语句，避免恢复到错误的库。
  await pipeline(Readable.from(`-- ---- ${schema} ----\nUSE \`${schema}\`;\n`), gzip, { end: false });
  const { cmd, prefix } = resolveCli('mysqldump');
  const child = spawn(cmd, [...prefix, ...dumpArgs, schema], { cwd: ROOT, env: nativeEnv, stdio: ['ignore', 'pipe', 'inherit'] });
  const done = pipeline(child.stdout, gzip, { end: false });
  const code = await new Promise((resolve, reject) => {
    child.on('exit', resolve);
    child.on('error', reject);
  });
  await done;
  if (code !== 0) fail(`mysqldump ${schema} failed; partial archive at ${archive} is not usable`);
}
await pipeline(Readable.from('-- end of backup\n'), gzip);
await finished(sink);

// 完整解压归档，检查是否截断或损坏。
let bytes = 0;
for await (const chunk of createReadStream(archive).pipe(createGunzip())) bytes += chunk.length;
const size = statSync(archive).size;
if (size < 1024) fail(`backup is suspiciously small (${size} bytes); refusing to report success`);

console.log(archive);
console.log(`verified: ${size} bytes, schemas: ${SCHEMAS.join(' ')}`);
