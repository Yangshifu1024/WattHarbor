// 扩展白名单月分区；默认预览，--apply 执行。仅重组 p_max，不删除或清空分区。
// 同一 MySQL 会话持有命名锁；并发维护被拒绝，错误和连接关闭自动释放锁。
import { openMySQL } from './mysql-client.mjs';

const args = process.argv.slice(2);
const apply = args.includes('--apply');
const throughArg = args.find(arg => /^--through=\d{4}-\d{2}$/.test(arg));
if (args.some(arg => arg !== '--apply' && arg !== throughArg) || args.filter(arg => arg === '--apply').length > 1) {
  throw new Error('用法：node scripts/db/maintain-partitions.mjs [--through=YYYY-MM] [--apply]');
}
const now = new Date();
const through = throughArg ? new Date(`${throughArg.slice(10)}-01T00:00:00Z`) : new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth()+12, 1));
if (Number.isNaN(through.getTime()) || throughArg && through.toISOString().slice(0,7) !== throughArg.slice(10)) throw new Error('Invalid month');
if (through.getUTCFullYear() > now.getUTCFullYear()+5) throw new Error('最多提前扩展5年');
const allowed = {
  central_db: ['charge_order','payment_order','refund_record','wallet_txn','audit_log','alert_event','fee_calculation'],
  gateway_db: ['telemetry','telemetry_aggregate_15min','telemetry_aggregate_hourly','device_session'],
  worker_db: ['task_execution_log'],
};
const db = openMySQL();
try {
  const lock = await db.query("SELECT GET_LOCK('chargepilot_partition_maintenance',0); SET SESSION lock_wait_timeout=10; SET SESSION time_zone='+00:00';");
  if (lock[0] !== '1') throw new Error('另一维护会话持有分区锁');
  let planned = 0;
  for (const [schema,tables] of Object.entries(allowed)) for (const table of tables) {
    const rows = await db.query(`SELECT partition_name,partition_description,partition_expression FROM information_schema.partitions WHERE table_schema='${schema}' AND table_name='${table}' ORDER BY partition_ordinal_position;`);
    const parts = rows.map(row => row.split('\t'));
    if (!parts.length || parts.some(p => p[0] === 'NULL')) throw new Error(`${schema}.${table}: 缺少预期分区`);
    if (parts.at(-1)[0] !== 'p_max' || parts.at(-1)[1] !== 'MAXVALUE' || !/^to_days\(`?(ts|bucket_month|created_month)`?\)$/i.test(parts[0][2])) throw new Error(`${schema}.${table}: 分区设计不符合白名单`);
    const last = parts.at(-2)?.[1];
    if (!/^\d+$/.test(last || '')) throw new Error('Invalid partition boundary');
    const [date] = await db.query(`SELECT DATE_FORMAT(FROM_DAYS(${last}),'%Y-%m-%d');`);
    const start = new Date(`${date}T00:00:00Z`);
    if (start.getUTCDate() !== 1 || Number.isNaN(start.getTime())) throw new Error('分区边界必须为月首');
    const additions = [];
    for (let month = start; month <= through; month = new Date(Date.UTC(month.getUTCFullYear(),month.getUTCMonth()+1,1))) {
      const name = `p_${month.getUTCFullYear()}m${String(month.getUTCMonth()+1).padStart(2,'0')}`;
      if (parts.some(p => p[0] === name)) throw new Error(`Existing partition name: ${name}`);
      const next = new Date(Date.UTC(month.getUTCFullYear(),month.getUTCMonth()+1,1)).toISOString().slice(0,10);
      additions.push(`PARTITION ${name} VALUES LESS THAN (TO_DAYS('${next}'))`);
    }
    if (!additions.length) continue;
    planned += additions.length;
    const sql = `ALTER TABLE \`${schema}\`.\`${table}\` REORGANIZE PARTITION p_max INTO (${additions.join(',')},PARTITION p_max VALUES LESS THAN MAXVALUE);`;
    console.log(`${schema}.${table}: ${additions.length} 个月分区${apply ? '，执行' : '，预览'}`);
    if (apply) await db.query(sql);
  }
  console.log(`合计 ${planned} 个新月分区；未删除历史数据。`);
  await db.query("SELECT RELEASE_LOCK('chargepilot_partition_maintenance');");
} finally { db.close(); }
