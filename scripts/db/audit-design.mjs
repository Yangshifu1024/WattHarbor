// 只读台账对账，不自动修账。存在差异返回 2，客户端/SQL 故障返回 1。
// 导入或种子余额未生成期初流水也会产生差异，须核对来源后处理。
import { readFileSync } from 'node:fs';
import { openMySQL } from './mysql-client.mjs';

const flags = process.argv.slice(2);
if (flags.length && (flags.length !== 1 || flags[0] !== '--unified')) {
  throw new Error('用法：node scripts/db/audit-design.mjs [--unified]');
}
const db = openMySQL();
try {
  const queries = readFileSync(new URL('./audit-design.sql', import.meta.url), 'utf8');
  const rows = await db.query(flags.length ? queries.replaceAll('central_db.', 'wattharbor.') : queries);
  let differences = 0;
  for (const row of rows) {
    const [name, count] = row.split('\t');
    if (!/^\d+$/.test(count || '')) throw new Error('Unexpected audit result');
    console.log(`${name}: ${count}`);
    differences += Number(count);
  }
  if (!rows.length) throw new Error('No audit results');
  if (differences) process.exitCode = 2;
} finally { db.close(); }
