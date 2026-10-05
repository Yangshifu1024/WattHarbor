// 把 ChargePilot 的三个库备份进同一个压缩包，并验证它能被完整读回来。
// 没人恢复过的备份不算备份，所以归档读不出来时直接报错退出，而不是报告成功。
// gzip 由 node:zlib 提供，不依赖外部 gzip 命令；MySQL 客户端可用宿主机
// mysql/mysqldump，或经 MYSQL_CLIENT_CONTAINER 在数据库容器内执行。
//
// 用法：
//   node scripts/db/backup.mjs <output-dir>
//   MYSQL_HOST=... MYSQL_PORT=... MYSQL_USER=... MYSQL_PWD=... node scripts/db/backup.mjs out/
//   MYSQL_CLIENT_CONTAINER=chargepilot-dev-mysql-1 MYSQL_PASSWORD=... node scripts/db/backup.mjs out/
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

// 文档约定原生客户端凭 MYSQL_PWD；调用方只给了 MYSQL_PASSWORD 时代为映射，
// 与容器回退路径的传参方式保持一致。
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
  return spawnSync(cmd, [...prefix, ...args], { cwd: ROOT, encoding: 'utf8' });
}

// 输出目录必须已存在：备份位置由操作者显式决定，不替他挑。
if (!existsSync(outDir) || !statSync(outDir).isDirectory()) {
  fail(`create output directory first: ${outDir}`);
}

// mysqldump 遇到缺表就会拒绝继续，所以还没建出来的库不能被悄悄跳过：
// 任一 schema 不存在即整包拒绝，绝不产出部分备份。
for (const schema of SCHEMAS) {
  const probe = runCli('mysql', ['-N', '-e', `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='${schema}'`]);
  if (probe.status !== 0 || probe.stdout.trim() === '') {
    fail(`schema ${schema} does not exist; refusing to produce a partial backup`);
  }
}

const archive = join(outDir, `chargepilot-${stamp}.sql.gz`);
// set-gtid-purged/column-statistics/no-tablespaces 与容器内外客户端版本差异相关，
// 与原 bash 版保持同一组参数，保证归档可在两个环境间互换。
const dumpArgs = ['--single-transaction', '--routines', '--triggers', '--events', '--set-gtid-purged=OFF', '--column-statistics=0', '--no-tablespaces'];

// gzip 变换流接到文件流上；注意 pipe() 返回终点流，必须保留 gzip 引用本身，
// 否则后续写入会绕过压缩（这正是归档头检查失败的原因）。
// 同一 gzip 流会被五段来源顺序写入，管道层会临时挂多个监听器，放宽告警阈值。
const gzip = createGzip({ level: 9 }).setMaxListeners(25);
const sink = createWriteStream(archive);
gzip.pipe(sink);

await pipeline(Readable.from(`-- ChargePilot backup ${stamp}\n-- schemas: ${SCHEMAS.join(' ')}\n`), gzip, { end: false });

for (const schema of SCHEMAS) {
  // USE 语句必须与该库的 dump 交错出现：全部前置会让所有 CREATE TABLE
  // 落进最后一个 USE 的库上下文（原 bash 版正是逐段交错，这里保持一致）。
  // 告诉 mysqldump 不要写 USE 时它就不写，所以这里显式声明目标库；
  // 靠猜的恢复会写进错误的库。
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

// 靠完整解压来验证；被截断的 dump 不应该算通过。
let bytes = 0;
for await (const chunk of createReadStream(archive).pipe(createGunzip())) bytes += chunk.length;
const size = statSync(archive).size;
if (size < 1024) fail(`backup is suspiciously small (${size} bytes); refusing to report success`);

console.log(archive);
console.log(`verified: ${size} bytes, schemas: ${SCHEMAS.join(' ')}`);
