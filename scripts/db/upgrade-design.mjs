// 对现有开发库执行本次数据库设计兼容更新。默认只预检；--apply 先备份再更新。
// DDL 从当前 0001_init.sql 中提取指定字段、索引和约束，不建立新的 Goose 版本。
// 不删除或合并记录；月分区键仅在所有联合唯一键预检无冲突后归一到月首。
// 发现冲突即停止，DDL 中断后可再次执行。
import { spawnSync } from 'node:child_process';
import { readFileSync, mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const root = fileURLToPath(new URL('../..', import.meta.url));
const apply = process.argv.slice(2).includes('--apply');
// review-fixes 仅处理本报告新增结构，避免顺带更新既有设计项；默认仍只预检。
const reviewFixes = process.argv.slice(2).includes('--review-fixes');
if (process.argv.slice(2).some(arg => !['--apply', '--review-fixes'].includes(arg))) throw new Error('用法：node scripts/db/upgrade-design.mjs [--review-fixes] [--apply]');
const compose = ['compose', '-f', 'docker/compose.dev.yaml'];
// 仅连接仓库开发编排，不从日志或命令行传递密码。
function sql(statement) {
  const result = spawnSync('docker', [...compose, 'exec', '-T', 'mysql', 'sh', '-c',
    'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --default-character-set=utf8mb4 --batch --skip-column-names -uroot'],
  { cwd: root, input: statement, encoding: 'utf8' });
  if (result.status !== 0) throw new Error(result.stderr || 'MySQL operation failed');
  return result.stdout.trim();
}

const checks = [
  ['gateway_db.telemetry', 'SELECT device_id,COALESCE(port_no,0),metric,ts FROM gateway_db.telemetry GROUP BY device_id,COALESCE(port_no,0),metric,ts HAVING COUNT(*)>1'],
  ['central_db.user', 'SELECT BINARY openid FROM central_db.user WHERE deleted_at IS NULL GROUP BY BINARY openid HAVING COUNT(*)>1'],
  ['gateway_db.device', 'SELECT device_id FROM gateway_db.device WHERE deleted_at IS NULL GROUP BY device_id HAVING COUNT(*)>1'],
  ['gateway_db.device_port', 'SELECT port_code FROM gateway_db.device_port WHERE deleted_at IS NULL GROUP BY port_code HAVING COUNT(*)>1'],
  ['central_db.device_meta', 'SELECT device_id FROM central_db.device_meta WHERE deleted_at IS NULL GROUP BY device_id HAVING COUNT(*)>1'],
];
for (const [label, query] of reviewFixes ? [] : checks) {
  const count = Number(sql(`SELECT COUNT(*) FROM (${query}) AS conflicts;`));
  if (count) throw new Error(`${label}: ${count} 组身份冲突；保留全部记录，须先人工核对。`);
}

const operations = [];
for (const schema of ['central_db', 'gateway_db', 'worker_db']) {
  const source = readFileSync(join(root, 'backend/migrations', schema, '0001_init.sql'), 'utf8');
  for (const match of source.matchAll(/CREATE TABLE `(\w+)` \(([\s\S]*?)\n\) ENGINE/g)) {
    const [, table, body] = match;
    const target = `\`${schema}\`.\`${table}\``;
    if (schema === 'gateway_db' && table === 'sync_delivery_retry') {
      if (sql(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='${schema}' AND table_name='${table}';`) === '0') {
        const end = source.indexOf(';', match.index);
        operations.push(source.slice(match.index, end + 1).replace(`CREATE TABLE \`${table}\``, `CREATE TABLE ${target}`));
      } else if (sql(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='${schema}' AND table_name='${table}' AND index_name='idx_updated';`) === '0') {
        const line = body.split('\n').map(value => value.trim().replace(/,$/, '')).find(value => value.startsWith('KEY `idx_updated`'));
        operations.push(`ALTER TABLE ${target} ADD ${line};`);
      }
      continue;
    }
    if (reviewFixes && !(schema === 'central_db' && ['admin_user', 'charge_billing_job'].includes(table))) continue;
    for (const line of body.split('\n').map(value => value.trim().replace(/,$/, ''))) {
      const column = /^`(\w+)`/.exec(line);
      const index = /^(?:UNIQUE )?KEY `(\w+)`/.exec(line);
      const constraint = /^CONSTRAINT `(chk_\w+)` CHECK \((.*)\)$/.exec(line);
      const reviewColumn = schema === 'central_db' && column &&
        (table === 'admin_user' && ['mfa_secret_version', 'mfa_last_step'].includes(column[1]) || table === 'charge_billing_job' && column[1] === 'generation');
      if (reviewColumn) {
        if (sql(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='${schema}' AND table_name='${table}' AND column_name='${column[1]}';`) === '0') operations.push(`ALTER TABLE ${target} ADD COLUMN ${line};`);
        continue;
      }
      if (reviewFixes) continue;
      if (constraint) {
        const bad = Number(sql(`SELECT COUNT(*) FROM ${target} WHERE NOT (${constraint[2]});`));
        if (bad && constraint[2] === 'DAYOFMONTH(created_month) = 1') {
          // 归一前复核所有包含月份的唯一键；存在碰撞时保留两条原记录并退出。
          for (const key of body.matchAll(/(?:PRIMARY KEY|UNIQUE KEY `\w+`) \(([^\n]+)\)/g)) {
            const columns = key[1].split(',');
            if (!columns.includes('`created_month`')) continue;
            const normalized = columns.map(name => name === '`created_month`' ? "DATE_FORMAT(created_month,'%Y-%m-01')" : name).join(',');
            const conflicts = Number(sql(`SELECT COUNT(*) FROM (SELECT ${normalized} FROM ${target} GROUP BY ${normalized} HAVING COUNT(*)>1) AS collisions;`));
            if (conflicts) throw new Error(`${schema}.${table}: 月首归一存在 ${conflicts} 组唯一键冲突；未修改数据。`);
          }
          operations.push(`UPDATE ${target} SET created_month=DATE_FORMAT(created_month,'%Y-%m-01') WHERE DAYOFMONTH(created_month)<>1;`);
        } else if (bad) {
          throw new Error(`${schema}.${table}: ${bad} 条记录不满足 ${constraint[1]}；未修改数据。`);
        }
        if (sql(`SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema='${schema}' AND table_name='${table}' AND constraint_name='${constraint[1]}';`) === '0') {
          operations.push(`ALTER TABLE ${target} ADD ${line};`);
        }
      } else if (column && (column[1].startsWith('live_') || table === 'telemetry' && column[1] === 'port_key')) {
        if (sql(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='${schema}' AND table_name='${table}' AND column_name='${column[1]}';`) === '0') {
          operations.push(`ALTER TABLE ${target} ADD COLUMN ${line};`);
        }
      } else if (column && ['openid', 'unionid'].includes(column[1]) && line.includes('varbinary(64)')) {
        if (sql(`SELECT data_type FROM information_schema.columns WHERE table_schema='${schema}' AND table_name='${table}' AND column_name='${column[1]}';`) !== 'varbinary') {
          operations.push(`ALTER TABLE ${target} MODIFY COLUMN ${line};`);
        }
      } else if (index && ['uk_live_openid', 'uk_live_device_id', 'uk_live_port_code', 'uk_sample', 'idx_device_ts', 'idx_device_time', 'idx_port_expiry', 'idx_order_started', 'idx_order_created', 'idx_user_ongoing', 'idx_pending_type'].includes(index[1])) {
        if (sql(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='${schema}' AND table_name='${table}' AND index_name='${index[1]}';`) === '0') {
          operations.push(`ALTER TABLE ${target} ADD ${line};`);
        }
      }
    }
  }
}
console.log(`预检通过，待执行 ${operations.length} 项兼容更新。`);
if (apply && operations.length) {
  const directory = join(root, '.tmp/db-design-backups');
  mkdirSync(directory, { recursive: true });
  const backup = spawnSync('node', ['scripts/db/backup.mjs', directory], { cwd: root, stdio: 'inherit',
    env: { ...process.env, MYSQL_CLIENT_CONTAINER: 'chargepilot-dev-mysql-1', MYSQL_PASSWORD: 'chargepilot_root_dev' } });
  if (backup.status !== 0) throw new Error('完整备份失败，未执行更新。');
  for (const operation of operations) {
    sql(`SET SESSION lock_wait_timeout=10; SET SESSION time_zone='+00:00';\n${operation}`);
    console.log(operation.replace(/ COMMENT '[^']*'/g, ''));
  }
  console.log('兼容更新完成；业务数据和 Goose 版本记录保留。');
}
