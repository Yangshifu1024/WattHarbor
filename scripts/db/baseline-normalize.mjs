// 确定性基线的数据固定层：消除造数过程中的运行时刻与 ID 随机量，使改造前后
// 两侧数据库内容完全一致，从而可以对接口响应做逐字节比较。
//
// 两侧各自独立执行本脚本，对同一份 devseed 输出产生完全相同的规范化结果：
//   时间列：整体平移到固定基准日，保留行间先后关系，只消除「什么时候跑的」。
//   ID 列：按「表内主键升序的行号」锚定重映射为固定值。两侧同一张表的第 N 行
//           得到同一个固定 ID，因此不需要推断外键指向——只要两侧插入顺序一致
//           （devseed 已验证两次运行顺序相同），同行同位即同一逻辑行。
//
// 为何不用值匹配：devseed 造的 wallet_account 每个用户有两行（uk_user 与
// uk_user_live 各一行），值匹配会把两行写成同一个 user_id 而触发唯一索引冲突。
// 行号锚定不关心值，只关心位置，天然规避该问题并保持引用完整性。
//
// 本脚本只处理含造数数据的表，不触碰其他行。
// 用法：
//   MYSQL_CLIENT_CONTAINER=wattharbor-base-mysql MYSQL_PASSWORD=... MYSQL_USER=root \
//     node scripts/db/baseline-normalize.mjs
import { openMySQL } from './mysql-client.mjs';

// 固定业务库。openMySQL 不选默认库，DATABASE() 为空，故显式限定。
const SCHEMA = 'central_db';
// 基准日：两侧造数时间最终都归一到此日。可由 BASELINE_DATE 覆盖，
// 使不同 commit 的 devseed（一个用注入时钟、一个靠平移）能落到同一时刻。
const BASE = process.env.BASELINE_DATE || '2026-01-15 00:00:00';
// 固定 ID 起点与每表步长。落在 bigint unsigned 范围内且远小于真实 Snowflake。
// 每表独占一段，跨表不相交，否则不同表的固定 ID 会撞号。
const ID_BASE = 9000000000000000001n;
const ID_STRIDE = 100000n;
// 造数数据前缀（devseed 的清理与识别范围）。
const DEMO = 'demo_';

const mysql = openMySQL();

function fail(message) {
  console.error(message);
  process.exit(1);
}

async function businessTables() {
  const rows = await mysql.query(
    `SELECT table_name FROM information_schema.tables
      WHERE table_schema = '${SCHEMA}' AND table_type = 'BASE TABLE'
        AND table_name NOT LIKE 'goose%' AND table_name <> 'snowflake_state'
      ORDER BY table_name;`);
  return rows.map(r => r.trim()).filter(Boolean);
}

async function columnsOf(table) {
  const rows = await mysql.query(
    `SELECT column_name, data_type, column_key, extra
       FROM information_schema.columns
      WHERE table_schema = '${SCHEMA}' AND table_name = '${table}'
        AND data_type IN ('datetime','timestamp','date','bigint','varchar','char')
      ORDER BY ordinal_position;`);
  return rows.map(r => r.split('\t')).map(([name, type, key, extra]) => ({
    name, type, isPrimary: key === 'PRI', autoIncrement: /auto_increment/i.test(extra || ''),
  }));
}

// 该表是否含造数数据：只看文本列的 demo_ 前缀。
// 不能用「时间列非空」做判据：那会把 permission、role_permission 等框架表也算进来，
// 而它们的行数在两次运行间可能不同，会破坏行号锚定的前提。
async function hasSeedRows(table, cols) {
  const probes = cols.filter(c => ['varchar', 'char'].includes(c.type)).slice(0, 16).map(c =>
    `EXISTS (SELECT 1 FROM \`${SCHEMA}\`.\`${table}\` WHERE \`${c.name}\` LIKE '${DEMO}%')`);
  if (!probes.length) return false;
  const rows = await mysql.query(`SELECT CASE WHEN (${probes.join(' OR ')}) THEN 1 ELSE 0 END;`);
  return rows[0]?.trim() === '1';
}

async function globalEarliest(tables, colsByTable) {
  let earliest = null;
  for (const table of tables) {
    for (const c of colsByTable[table] || []) {
      if (!['datetime', 'timestamp', 'date'].includes(c.type)) continue;
      const rows = await mysql.query(
        `SELECT DATE_FORMAT(MIN(\`${c.name}\`), '%Y-%m-%d %H:%i:%s') FROM \`${SCHEMA}\`.\`${table}\`;`);
      const v = rows[0]?.trim();
      if (v && (!earliest || v < earliest)) earliest = v;
    }
  }
  return earliest;
}

// 秒级的全局最早时间。平移量必须两侧完全相同，因此只取到秒（不含随机毫秒）。
async function globalEarliestSeconds(tables, colsByTable) {
  let earliest = null;
  for (const table of tables) {
    for (const c of colsByTable[table] || []) {
      if (!['datetime', 'timestamp', 'date'].includes(c.type)) continue;
      const rows = await mysql.query(
        `SELECT DATE_FORMAT(MIN(\`${c.name}\`), '%Y-%m-%d %H:%i:%s') FROM \`${SCHEMA}\`.\`${table}\`;`);
      const v = rows[0]?.trim();
      if (v && (!earliest || v < earliest)) earliest = v;
    }
  }
  return earliest;
}

async function main() {
  const tables = await businessTables();
  const colsByTable = {};
  for (const t of tables) colsByTable[t] = await columnsOf(t);

  const targets = [];
  for (const t of tables) {
    if (await hasSeedRows(t, colsByTable[t])) targets.push(t);
  }
  if (!targets.length) fail('未找到任何造数数据，请先运行 devseed。');

  const earliest = await globalEarliest(targets, colsByTable);
  if (!earliest) fail('未找到任何时间列值，无法计算平移基准。');
  // 按毫秒平移：devseed 写入的是 datetime(3)，若只保留秒级精度，
  // 两侧的毫秒部分会残留差异（如 19:58:05.281 与 19:58:05.775），
  // 导致数据哈希不一致。TIMESTAMPDIFF(MICROSECOND) 可保留毫秒。
  //
  // 平移量本身必须两侧相同，故不能取「实际数据的最早时间」——
  // 那个值含随机毫秒，两侧各取各的会让结果错位。
  // 改为先按秒级把整库对齐到 BASE，再按行号把毫秒尾数重写为确定值。
  const shiftSeconds = await globalEarliestSeconds(targets, colsByTable);
  const shift = `TIMESTAMPDIFF(SECOND, '${shiftSeconds}', '${BASE}')`;
  console.log(`造数表 ${targets.length} 张；平移 ${shift} 秒`);

// 行号锚定映射：tbl / row_no / old_id / new_id。
  // 行号锚定映射：tbl / row_no / old_id / new_id。
  // row_no 是该表按主键升序的 0-based 行号；两侧一致即可保证同行同位。
  await mysql.query(
    `CREATE TEMPORARY TABLE IF NOT EXISTS \`${SCHEMA}\`.baseline_row_map (
       tbl VARCHAR(64) NOT NULL, row_no INT NOT NULL, old_id BIGINT NOT NULL, new_id BIGINT NOT NULL,
       KEY (tbl), KEY (old_id), KEY (new_id));`);
  await mysql.query(`TRUNCATE TABLE \`${SCHEMA}\`.baseline_row_map;`);

  const pkOf = {};
  const ordered = targets.slice().sort();
  for (const [tableIndex, table] of ordered.entries()) {
    const pk = colsByTable[table].find(c => c.isPrimary && c.type === 'bigint');
    if (!pk) continue;
    pkOf[table] = pk.name;
    const rows = await mysql.query(
      `SELECT CAST(\`${pk.name}\` AS CHAR) FROM \`${SCHEMA}\`.\`${table}\` ORDER BY \`${pk.name}\`;`);
    let rowNo = 0;
    for (const r of rows) {
      const oldId = r.trim();
      if (!oldId) continue;
      const newId = String(ID_BASE + ID_STRIDE * BigInt(tableIndex) + BigInt(rowNo));
      await mysql.query(
        `INSERT INTO \`${SCHEMA}\`.baseline_row_map (tbl, row_no, old_id, new_id)
         VALUES ('${table}', ${rowNo}, ${oldId}, ${newId});`);
      rowNo += 1;
    }
  }
  const mapped = await mysql.query(`SELECT COUNT(*) FROM \`${SCHEMA}\`.baseline_row_map;`);
  console.log(`行号映射 ${mapped[0]?.trim()} 条，覆盖 ${Object.keys(pkOf).length} 张表`);

  // 先改主键：按表逐行精确替换。旧 ID 可能与新 ID 重叠，故先统一加偏移量再回调，
  // 确保单次 UPDATE 内不会把已改写的行再次命中。
  for (const [table, pkCol] of Object.entries(pkOf)) {
    const rows = await mysql.query(
      `SELECT old_id, new_id FROM \`${SCHEMA}\`.baseline_row_map WHERE tbl = '${table}' ORDER BY row_no;`);
    // 从大到小改写：新 ID 全部大于旧 ID，倒序可避免「改小的之后又命中待改的」。
    const pairs = rows.map(r => r.split('\t').map(s => s.trim())).filter(p => p[0] && p[1]);
    pairs.sort((a, b) => Number(b[0]) - Number(a[0]));
    for (const [oldId, newId] of pairs) {
      await mysql.query(
        `UPDATE \`${SCHEMA}\`.\`${table}\` SET \`${pkCol}\` = ${newId} WHERE \`${pkCol}\` = ${oldId};`);
    }
  }

  // 再改外键：按「目标表 + 旧值」精确匹配。用值匹配而非行号，
  // 因为外键的旧值确实来自目标表主键；行号锚定保证目标表的新值两侧一致。
  for (const table of targets) {
    const cols = colsByTable[table];
    for (const c of cols) {
      if (c.type !== 'bigint' || c.autoIncrement || c.name === pkOf[table]) continue;
      if (!/_id$/.test(c.name)) continue;
      const ref = c.name.replace(/_id$/, '');
      if (!Object.prototype.hasOwnProperty.call(pkOf, ref)) continue;
      const rows = await mysql.query(
        `SELECT old_id, new_id FROM \`${SCHEMA}\`.baseline_row_map WHERE tbl = '${ref}' ORDER BY row_no;`);
      const pairs = rows.map(r => r.split('\t').map(s => s.trim())).filter(p => p[0] && p[1]);
      // 同样倒序改写，避免中间态二次命中。
      pairs.sort((a, b) => Number(b[0]) - Number(a[0]));
      for (const [oldId, newId] of pairs) {
        await mysql.query(
          `UPDATE \`${SCHEMA}\`.\`${table}\` SET \`${c.name}\` = ${newId} WHERE \`${c.name}\` = ${oldId};`);
      }
      console.log(`  外键 ${table}.${c.name} -> ${ref} 已改写 ${pairs.length} 组`);
    }
  }

// 文本型随机量：uuid v4 形式的单号（intent_id、client_request_id 等）。
  // 放在 ID 改写之后，按行号锚定重写为确定性 UUID 文本，两侧同行同值。
  for (const table of targets) {
    for (const c of colsByTable[table].filter(x => ['varchar', 'char'].includes(x.type))) {
      const pattern = `'^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'`;
      const hit = await mysql.query(
        `SELECT COUNT(*) FROM \`${SCHEMA}\`.\`${table}\` WHERE \`${c.name}\` REGEXP ${pattern};`);
      if (hit[0]?.trim() === '0') continue;
      // 该表可能没有 bigint 主键（如以文本单号为主键），此时按所有列排序保证行序稳定。
      const orderBy = pkOf[table]
        ? `\`${pkOf[table]}\``
        : (await mysql.query(
             `SELECT column_name FROM information_schema.columns
               WHERE table_schema = '${SCHEMA}' AND table_name = '${table}' ORDER BY ordinal_position;`))
            .map(r => r.trim()).filter(Boolean).slice(0, 3).map(n => `\`${n}\``).join(',');
      const rows = await mysql.query(
        `SELECT \`${c.name}\` FROM \`${SCHEMA}\`.\`${table}\` ORDER BY ${orderBy};`);
      for (const [rowNo, r] of rows.entries()) {
        // 由行号派生确定性 UUID，保持 8-4-4-4-12 的十六进制形状。
        const h = n => ((n * 2654435761) % 0xffffffff).toString(16).toUpperCase().padStart(8, '0');
        const short = n => ((n * 2246822519) % 0xffff).toString(16).toUpperCase().padStart(4, '0');
        const seed = rowNo + 1;
        const uuid = `${h(seed)}-${short(seed + 1)}-${short(seed + 2)}-9${short(seed + 3).slice(1)}-${h(seed + 7).slice(0, 12)}`;
        await mysql.query(
          `UPDATE \`${SCHEMA}\`.\`${table}\` SET \`${c.name}\` = '${uuid}' WHERE \`${c.name}\` = '${r.trim()}';`);
      }
      console.log(`  单号 ${table}.${c.name} 已按行号固定`);
    }
  }

  // 时间列：按秒平移，保留行间相对关系。
  for (const table of targets) {
    for (const c of colsByTable[table]) {
      if (!['datetime', 'timestamp'].includes(c.type)) continue;
      await mysql.query(
        `UPDATE \`${SCHEMA}\`.\`${table}\` SET \`${c.name}\` = DATE_ADD(\`${c.name}\`, INTERVAL ${shift} SECOND)
          WHERE \`${c.name}\` IS NOT NULL;`);
    }
  }

  // 秒与毫秒尾数均按行号重写为确定值。
  // 平移只能对齐到「秒级基准」，而 devseed 的 daysAgo 偏移是基于各自运行时刻算的，
  // 同一逻辑时刻在两次运行间会跨秒边界（如 20:03:44 与 20:03:46）。
  // 因此只保留平移后的「日期与时分」，秒与毫秒按行号重新派生，两侧即可完全一致。
  for (const table of targets) {
    const cols = colsByTable[table];
    const timeCols = cols.filter(c => ['datetime', 'timestamp'].includes(c.type));
    if (!timeCols.length) continue;
    const orderBy = pkOf[table]
      ? `\`${pkOf[table]}\``
      : cols.slice(0, 2).map(n => `\`${n.name}\``).join(',');
    const rows = await mysql.query(
      `SELECT ${timeCols.map(c => `CAST(\`${c.name}\` AS CHAR)`).join(', ')}
         FROM \`${SCHEMA}\`.\`${table}\` ORDER BY ${orderBy};`);
    for (const [rowNo, r] of rows.entries()) {
      // 逐列取值时必须按 tab 分隔原样切；NULL 会被 mysql 客户端渲染成字面量 NULL。
      const values = r.split('\t').map(v => (v || '').trim());
      // 同一行的所有时间列保持同一秒与毫秒，同时保留各自原本的日期时分。
      const m = (values[0] || '').match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}):(\d{2}):(\d{2})/);
      if (!m) continue;
      const [, date, hh, mm] = m;
      const sec = String((rowNo * 7) % 60).padStart(2, '0');
      const millis = String((rowNo * 37) % 1000).padStart(3, '0');
      const stamp = `${date} ${hh}:${mm}:${sec}.${millis}`;
      // 按当前值逐列定位，避免同表内多行共享同一时间戳时误改。
      for (const [i, c] of timeCols.entries()) {
        const value = values[i];
        // NULL 在客户端输出中表现为字面量 NULL，必须跳过，
        // 否则 STR_TO_DATE('NULL') 会报 Incorrect datetime value。
        if (!value || value === 'NULL') continue;
        await mysql.query(
          `UPDATE \`${SCHEMA}\`.\`${table}\` SET \`${c.name}\` = STR_TO_DATE('${stamp}', '%Y-%m-%d %H:%i:%s.%f')
            WHERE \`${c.name}\` = '${value}';`);
      }
    }
  }

  // 分区列与所属表的 created_at 对齐。
  for (const table of targets) {
    const cols = colsByTable[table];
    const monthCol = cols.find(c => c.type === 'date' && /created_month$/.test(c.name));
    const atCol = cols.find(c => c.type === 'datetime' && /^created_at$/.test(c.name));
    if (!monthCol || !atCol) continue;
    await mysql.query(
      `UPDATE \`${SCHEMA}\`.\`${table}\` SET \`${monthCol.name}\` = DATE_FORMAT(\`${atCol.name}\`, '%Y-%m-01')
        WHERE \`${atCol.name}\` IS NOT NULL;`);
  }

  const maxSlot = BigInt(ordered.length) * ID_STRIDE;
  const bad = await mysql.query(
    `SELECT COUNT(*) FROM \`${SCHEMA}\`.baseline_row_map
      WHERE new_id < ${ID_BASE} OR new_id >= ${ID_BASE} + ${maxSlot};`);
  if (bad[0]?.trim() !== '0') fail('固定 ID 越界，请检查 ID_BASE 或 ID_STRIDE。');
  const dup = await mysql.query(
    `SELECT COUNT(*) FROM (SELECT new_id FROM \`${SCHEMA}\`.baseline_row_map
       GROUP BY new_id HAVING COUNT(DISTINCT tbl) > 1) x;`);
  if (dup[0]?.trim() !== '0') fail('跨表固定 ID 重叠，请增大 ID_STRIDE。');

  console.log('数据固定完成。');
}

main()
  .then(() => mysql.close())
  .catch(error => { console.error(error); process.exit(1); });