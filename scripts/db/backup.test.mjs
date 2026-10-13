import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';

// 假客户端同时隔离探库、dump 和 Docker；真实执行备份脚本及压缩读回，不连接任何数据库。
for (const [name, password, pwd, expected] of [
  ['MYSQL_PASSWORD mapping', 'test-only-password', undefined, 'test-only-password'],
  ['MYSQL_PWD wins', 'unused-password', 'explicit-password', 'explicit-password'],
]) test(name, t => {
  const root = mkdtempSync(join(tmpdir(), 'chargepilot-backup-test-')); t.after(() => rmSync(root, { recursive: true, force: true }));
  const bin = join(root, 'bin'), out = join(root, 'out'), log = join(root, 'calls'); mkdirSync(bin); mkdirSync(out);
  const cli = `#!${process.execPath}\nconst fs=require('node:fs'),crypto=require('node:crypto');
const args=process.argv.slice(2),tool=process.argv[1].split('/').at(-1);
if(args.includes('--version'))process.exit(0);
if(process.env.MYSQL_PWD!==process.env.EXPECTED_PWD){console.error('credential mismatch');process.exit(1)}
fs.appendFileSync(process.env.CALL_LOG,tool+'\\n');
if(tool==='mysql'){const query=args[args.indexOf('-e')+1];process.stdout.write(query.match(/SCHEMA_NAME='([^']+)'/)[1]+'\\n');}
else if(tool==='mysqldump'){for(let i=0;i<300;i++)process.stdout.write('-- '+i+' '+crypto.createHash('sha256').update(String(i)).digest('hex')+'\\n');}
else process.exit(2);
`;
  for (const tool of ['mysql', 'mysqldump', 'docker']) writeFileSync(join(bin, tool), cli, { mode: 0o755 });
  const env = { PATH: bin, MYSQL_HOST: '127.0.0.1', MYSQL_USER: 'fake-user', MYSQL_PASSWORD: password, EXPECTED_PWD: expected, CALL_LOG: log };
  if (pwd) env.MYSQL_PWD = pwd;
  const result = spawnSync(process.execPath, [fileURLToPath(new URL('./backup.mjs', import.meta.url)), out], { env, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(log, 'utf8'), 'mysql\nmysql\nmysql\nmysqldump\nmysqldump\nmysqldump\n');
  const archive = readdirSync(out); assert.equal(archive.length, 1);
  const sql = gunzipSync(readFileSync(join(out, archive[0]))).toString();
  for (const schema of ['gateway_db', 'central_db', 'worker_db']) assert.ok(sql.includes('USE `'+schema+'`;'));
  assert.ok(sql.endsWith('-- end of backup\n'));
  assert.ok(!(result.stdout + result.stderr).includes(expected));
});
