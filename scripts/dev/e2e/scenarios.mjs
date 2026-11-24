// 在开发栈调用用户 API、运行 worker 任务，并通过 TCP 设备模拟器执行充电场景。
// 场景结束后查询开发数据库，检查订单、费用和退款记录。场景及断言说明见
// docs/operations.md「全链路场景验收」。
//
// 前置：开发栈已启动（node tasks.mjs dev-up）且 prepare.mjs 已建立厂商、站点、设备与
// 方案基础数据；测试直连开发环境（docker/compose.dev.yaml）。模拟器已按场景
// 以 -scenario <x> 接入宿主机 9100 端口（控制口 9190）。场景与模拟器场景的对应关系：
//   s3 → reject-start        s1 → by-time            s2 → stop-on-command
//   s5 → stop-on-command     s7 → stop-on-command    s4 → stop-on-command（先执行 budget）
//   s8 → reconnect
//
// 用法：node scripts/dev/e2e/scenarios.mjs <budget|s3|s1|s2|s5|s7|s4|s8>
//   budget  创建并应用「E2E预算方案」（10 分金额套餐），仅 s4 前需要执行一次。
// 断言统一取库中最新一笔订单，因此不得并行运行多个场景；每个场景自建订单、
// 轮询直到订单处理完成，输出 E2E-RESULT JSON；任何断言失败以非零码退出。
// 模拟支付与模拟器结果不构成实机、真实微信商户或监管投递的验收。
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../../..', import.meta.url)).replace(/\\$/, '');
const USER = 'http://127.0.0.1:8080/api/v1';
const ADMIN = 'http://127.0.0.1:8081/api/v1';
const SIM = 'http://127.0.0.1:9190';
// prepare.mjs 导入的 1 号模拟器设备的 1 号端口业务编码。
const PORT_CODE = '5348240514082652-1';
// 使用开发栈 Compose 配置；s7 强制停止 worker 容器后再启动。
const COMPOSE = ['compose', '-f', 'docker/compose.dev.yaml'];

// 将不安全的大整数按原 JSON 文本读为 BigInt，写请求时保留整数文本。
const readJSON = text => JSON.parse(text, (_k, v, ctx) =>
  typeof v === 'number' && Number.isInteger(v) && !Number.isSafeInteger(v) ? BigInt(ctx.source) : v);
const writeJSON = v => JSON.stringify(v, (_k, i) => typeof i === 'bigint' ? JSON.rawJSON(i.toString()) : i);

async function call(base, method, path, body, token) {
  const r = await fetch(base + path, {
    method,
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: 'Bearer ' + token } : {}) },
    ...(body ? { body: writeJSON(body) } : {}),
  });
  const j = readJSON(await r.text());
  if (!r.ok || j.code !== 0) throw new Error(`${method} ${path} -> ${r.status} ${writeJSON(j)}`);
  return j.data;
}

// 模拟器控制口仅监听 loopback；power 的 power_deciwatts 单位是分瓦，1500 表示 150 W。
const simEvent = async body => {
  const r = await fetch(`${SIM}/events`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  if (!r.ok) throw new Error(`模拟器控制 ${writeJSON(body)} -> ${r.status}`);
  return r.json();
};

function sql(db, text) {
  const out = execFileSync('docker', ['compose', '-f', 'docker/compose.dev.yaml', 'exec', '-T', 'mysql', 'sh', '-c',
    `MYSQL_PWD="$MYSQL_PASSWORD" mysql --protocol=TCP -uwattharbor ${db} -N -B -e '${text}'`],
    { encoding: 'utf8', cwd: ROOT });
  return out.trim().split('\n').filter(l => l !== '').map(l => l.split('\t'));
}

const sleep = ms => new Promise(r => setTimeout(r, ms));

async function until(fn, what, timeoutMs, everyMs = 1500) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const last = await fn();
    if (last.ok) return last.value;
    if (Date.now() > deadline) throw new Error(`等待 ${what} 超时：${writeJSON(last.value ?? last)}`);
    await sleep(everyMs);
  }
}

const latestOrder = () => sql('central_db',
  'SELECT id, order_no, status, started_at, ended_at, charged_kwh, charged_seconds FROM charge_order ORDER BY id DESC LIMIT 1');
const orderEvents = id => sql('central_db',
  `SELECT event FROM charge_event_log WHERE charge_order_id = ${id} ORDER BY id`);
// 本场景用 charge 业务的订单 ID 查询支付与退款；只按 biz_id 筛选，运行时须避免其他业务 ID 混入。
const orderRefunds = id => sql('central_db',
  `SELECT refund_cents, status FROM refund_record WHERE biz_id = ${id} ORDER BY id`);
const orderPayment = id => sql('central_db',
  `SELECT total_cents, refunded_cents, status FROM payment_order WHERE biz_id = ${id} ORDER BY id DESC LIMIT 1`);
const orderCutoff = id => sql('central_db',
  `SELECT reason, electric_cents, service_cents FROM charge_billing_cutoff WHERE charge_order_id = ${id} ORDER BY created_at DESC LIMIT 1`);
const orderJob = id => sql('central_db',
  `SELECT status, attempts FROM charge_billing_job WHERE charge_order_id = ${id}`);
// 费用回执的金额在 result_json（Fee 序列化）内，不在独立列上。
const orderFee = id => {
  const rows = sql('central_db',
    `SELECT result_json FROM charge_fee_receipt WHERE charge_order_id = ${id} ORDER BY created_at DESC LIMIT 1`);
  return rows.length ? JSON.parse(rows[0][0]) : null;
};
const orderEndReceipt = id => sql('central_db',
  `SELECT meter_json FROM charge_end_receipt WHERE charge_order_id = ${id}`);
const orderReviews = id => sql('central_db',
  `SELECT COUNT(*) FROM manual_fee_review WHERE charge_order_id = ${id}`);
const portOccupancy = id => sql('central_db',
  `SELECT COUNT(*) FROM active_port_charge WHERE charge_order_id = ${id}`);

function meterSummary(meterJson) {
  if (!meterJson) return null;
  const segs = JSON.parse(meterJson).segments ?? [];
  return { segments: segs.length, sumWh: segs.reduce((a, s) => a + (s.energy_wh ?? 0), 0),
    firstStart: segs[0]?.started_at ?? null, missingPower: segs.filter(s => s.power_w === undefined).length };
}

const rows1 = rows => rows.length
  ? Object.fromEntries(['id', 'order_no', 'status', 'started_at', 'ended_at', 'charged_kwh', 'charged_seconds'].map((k, i) => [k, rows[0][i]]))
  : null;

// 开发登录：dev:<账号> 仅 LOGIN_MODE=development 可用，首登自动建用户与零余额钱包。
async function login() {
  const out = await call(USER, 'POST', '/user/auth/login', { code: 'dev:e2e' });
  return out.token ?? out.jwt;
}

// 标准下单链路：匿名报价选套餐 → 登录创建支付 → 模拟确认到账；到账后 worker
// 经 paid_start 队列派发 START。offerMode 取报价项的 mode（amount/duration/energy）。
async function startCharge(token, offerMode) {
  const offers = await call(USER, 'POST', '/user/scan/offers', { port_id: PORT_CODE });
  const offer = offers.items.find(o => o.mode === offerMode);
  if (!offer) throw new Error('offers 无可用套餐：' + writeJSON(offers.items));
  const started = await call(USER, 'POST', '/user/scan/start', {
    client_request_id: crypto.randomUUID(), port_id: PORT_CODE, offer_id: offer.id,
  }, token);
  await call(USER, 'POST', '/user/development/payments/confirm', { merchant_order_no: started.merchant_order_no }, token);
  return { offers, offer, started };
}

const waitOrder = (check, what, timeoutMs) => until(async () => {
  const o = rows1(await latestOrder());
  return { ok: check(o), value: o };
}, what, timeoutMs);
const waitJobDone = id => until(async () => ({ ok: (await orderJob(id))[0]?.[0] === 'done', value: await orderJob(id) }), '计费任务 done', 90000);

function assert(cond, msg, acc) { if (!cond) acc.push(msg); }

function verdict(name, order, fail, detail) {
  const out = { scenario: name, order: order ?? null, verdict: fail.length ? 'FAIL' : 'PASS', fail, detail };
  console.log('E2E-RESULT ' + writeJSON(out));
  if (fail.length) process.exitCode = 1;
}

// budget：创建并应用 10 分金额套餐方案（S4 专用）。应用走乐观锁，
// expected_version 取站点当前生效版本；需要模拟器在线以解析站点。
async function budget() {
  const login0 = await call(ADMIN, 'POST', '/admin/auth/login', { username: 'admin', password: 'ChangeMe!Admin2026' });
  const token = login0.token ?? login0.access_token;
  const schemes = await call(ADMIN, 'GET', '/admin/settings/charging-schemes', null, token);
  let tpl = schemes.items.find(t => t.scheme.name === 'E2E预算方案');
  if (!tpl) {
    const base = schemes.items.find(t => t.scheme.name === '双模拟器测试方案')?.scheme;
    if (!base) throw new Error('找不到基准方案');
    const scheme = structuredClone(base);
    scheme.name = 'E2E预算方案';
    scheme.packages = scheme.packages.map(p => p.mode === 'amount' ? { ...p, name: '0.1元预算', price_cents: 10 } : p);
    tpl = await call(ADMIN, 'POST', '/admin/settings/charging-schemes', { scheme }, token);
  }
  const offers = await call(USER, 'POST', '/user/scan/offers', { port_id: PORT_CODE });
  const effective = await call(ADMIN, 'GET', `/admin/stations/${offers.station_id}/charging-scheme`, null, token);
  const applied = await call(ADMIN, 'POST', '/admin/settings/charging-schemes/apply', {
    request_id: crypto.randomUUID(), station_id: offers.station_id,
    template_id: tpl.id, template_version: tpl.version, expected_version: effective.version ?? 0,
  }, token);
  console.log('E2E-RESULT ' + writeJSON({ scenario: '预算方案应用', verdict: 'PASS', detail: applied }));
}

// S3 拒绝启动：设备返回拒绝回执后，检查订单全额退款。
async function s3() {
  const token = await login();
  const fail = [];
  await startCharge(token, 'amount');
  const o = await waitOrder(x => x?.status === 'refunded', '订单转 refunded', 90000);
  const id = o.id;
  const receipt = sql('central_db', `SELECT success, result_code FROM charge_start_receipt WHERE charge_order_id = ${id}`);
  const refunds = await orderRefunds(id);
  const pay = await orderPayment(id);
  const occ = await portOccupancy(id);
  const events = (await orderEvents(id)).map(r => r[0]);
  assert(receipt.length && receipt[0][0] === '0', `启动回执应为失败，得到 ${writeJSON(receipt)}`, fail);
  assert(refunds.length === 1 && refunds[0][1] === 'success', `应有一条 success 全额退款，得到 ${writeJSON(refunds)}`, fail);
  assert(pay.length && pay[0][2] === 'refunded', `支付单应 refunded，得到 ${writeJSON(pay)}`, fail);
  assert(occ[0][0] === '0', `拒绝起充不应建立端口占用，得到 ${occ[0][0]}`, fail);
  assert(events.includes('start_rejected'), '缺少 start_rejected 事件', fail);
  return verdict('S3 拒绝起充全额退款', o, fail, { receipt, refunds, pay, events });
}

// S1 时长套餐自然结束：设备按时长自停，自动计费无退款。
// 设备自行结束的订单不写服务端 charge_billing_cutoff——该表只记录服务端主动
// 停机（用户/自动/预算）的截止点；仅当服务端时长自动停机抢在设备结束帧之前
// 到达时才会出现 duration_exhausted 行，两种到达顺序都视为通过。
async function s1() {
  const token = await login();
  const fail = [];
  await startCharge(token, 'duration');
  const o = await waitOrder(x => x?.status === 'completed', '订单转 completed', 180000);
  const id = o.id;
  await waitJobDone(id);
  const cutoff = await orderCutoff(id);
  const job = await orderJob(id);
  const fee = await orderFee(id);
  const pay = await orderPayment(id);
  const refunds = await orderRefunds(id);
  const meter = meterSummary((await orderEndReceipt(id))[0]?.[0]);
  assert(cutoff.length === 0 || cutoff[0]?.[0] === 'duration_exhausted', `设备自然结束不写服务端截止或写 duration_exhausted，得到 ${writeJSON(cutoff)}`, fail);
  assert(job[0]?.[0] === 'done', `计费任务应 done，得到 ${writeJSON(job)}`, fail);
  assert(fee && Number(fee.total_cents) === 1, `费用应为 1 分，得到 ${writeJSON(fee)}`, fail);
  assert(refunds.length === 0, `预付等于实收不应有退款，得到 ${writeJSON(refunds)}`, fail);
  assert(pay[0][2] === 'paid', `支付单应 paid，得到 ${writeJSON(pay)}`, fail);
  assert(meter && meter.segments > 0, '结束回执缺少分段', fail);
  return verdict('S1 时长套餐自然结束', o, fail, { cutoff, job, fee, pay, refunds, meter });
}

// S2 用户主动停机：采样分段自动计费并自动退差额。
async function s2() {
  const token = await login();
  const fail = [];
  await startCharge(token, 'amount');
  const o = await waitOrder(x => x?.status === 'charging', '订单转 charging', 90000);
  const id = o.id;
  await simEvent({ type: 'power', port: 1, power_deciwatts: 2500 });
  await sleep(90000);
  await call(USER, 'POST', '/user/charge/stop', { order_no: o.order_no }, token);
  const done = await waitOrder(x => x?.status === 'completed', '订单转 completed', 120000);
  await waitJobDone(id);
  const cutoff = await orderCutoff(id);
  const job = await orderJob(id);
  const fee = await orderFee(id);
  const pay = await orderPayment(id);
  const refunds = await orderRefunds(id);
  const reviews = await orderReviews(id);
  const meter = meterSummary((await orderEndReceipt(id))[0]?.[0]);
  assert(cutoff[0]?.[0] === 'stop_request', `截止原因应 stop_request，得到 ${writeJSON(cutoff)}`, fail);
  assert(job[0]?.[0] === 'done' && Number(job[0]?.[1]) === 0, `计费应一次成功 done/0，得到 ${writeJSON(job)}`, fail);
  assert(reviews[0][0] === '0', '不应产生人工计量复核', fail);
  assert(fee && Number(fee.total_cents) > 0, `费用应大于零，得到 ${writeJSON(fee)}`, fail);
  assert(refunds.length === 1 && refunds[0][1] === 'success', `应有一条 success 差额退款，得到 ${writeJSON(refunds)}`, fail);
  if (refunds.length && fee) {
    assert(Number(refunds[0][0]) === Number(pay[0][0]) - Number(fee.total_cents),
      `退款 ${refunds[0][0]} 应等于预付 ${pay[0][0]} 减费用 ${fee.total_cents}`, fail);
  }
  assert(pay[0][2] === 'partial_refunded', `支付单应 partial_refunded，得到 ${writeJSON(pay)}`, fail);
  assert(meter && meter.segments > 0 && meter.missingPower === 0, `分段应全部带功率，得到 ${writeJSON(meter)}`, fail);
  return verdict('S2 用户停机自动计费差额退款', done, fail, { cutoff, job, fee, pay, refunds, meter });
}

// S5 零功率：启动后立即模拟拔出，连续一分钟功率为零后应自动停机并全额退款。
async function s5() {
  const token = await login();
  const fail = [];
  await startCharge(token, 'amount');
  const o = await waitOrder(x => x?.status === 'charging', '订单转 charging', 90000);
  const id = o.id;
  await simEvent({ type: 'unplug', port: 1 });
  const done = await waitOrder(x => ['completed', 'refunding', 'refunded'].includes(x?.status), '自动停机闭环', 420000);
  await waitJobDone(id);
  await until(async () => {
    const r = await orderRefunds(id);
    return { ok: r.length === 0 || r[0][1] === 'success', value: r };
  }, '退款闭环', 90000);
  const cutoff = await orderCutoff(id);
  const fee = await orderFee(id);
  const pay = await orderPayment(id);
  const refunds = await orderRefunds(id);
  assert(cutoff[0]?.[0] === 'automatic_stop', `截止原因应 automatic_stop，得到 ${writeJSON(cutoff)}`, fail);
  assert(fee && Number(fee.total_cents) === 0, `零电量费用应为 0 分，得到 ${writeJSON(fee)}`, fail);
  assert(refunds.length === 1 && refunds[0][1] === 'success' && Number(refunds[0][0]) === Number(pay[0][0]),
    `应全额退款，得到 ${writeJSON(refunds)} 预付 ${pay[0]?.[0]}`, fail);
  return verdict('S5 掉电零功率自动停机', done, fail, { cutoff, fee, pay, refunds });
}

// S7 worker 中断恢复：先强制停止 worker，再请求设备停止充电。
// worker 停止期间订单应保持 charging 且没有结束回执；重启后应保存结束回执并完成计费。
// 会话过短可能没有心跳，因此不检查分段数量。
async function s7() {
  const token = await login();
  const fail = [];
  await startCharge(token, 'amount');
  const o = await waitOrder(x => x?.status === 'charging', '订单转 charging', 90000);
  const id = o.id;
  execFileSync('docker', [...COMPOSE, 'kill', '--signal=KILL', 'worker'], { cwd: ROOT, stdio: 'pipe' });
  await sleep(3000);
  await call(USER, 'POST', '/user/charge/stop', { order_no: o.order_no }, token);
  // 等待 12 秒，检查 worker 停止期间订单是否仍为 charging 且没有结束回执。
  await sleep(12000);
  const stuck = rows1(await latestOrder());
  const stuckReceipt = await orderEndReceipt(id);
  assert(stuck.status === 'charging', `worker 缺席期间订单应保持 charging，得到 ${stuck.status}`, fail);
  assert(stuckReceipt.length === 0, 'worker 缺席期间不应出现结束回执', fail);
  execFileSync('docker', [...COMPOSE, 'up', '-d', '--wait', 'worker'], { cwd: ROOT, stdio: 'pipe' });
  const done = await waitOrder(x => x?.status === 'completed', '重启后订单转 completed', 120000);
  await waitJobDone(id);
  const events = (await orderEvents(id)).map(r => r[0]);
  const meter = meterSummary((await orderEndReceipt(id))[0]?.[0]);
  const fee = await orderFee(id);
  assert(events.includes('charge_ended'), '恢复后缺少 charge_ended 事件', fail);
  assert(meter !== null, '恢复后缺少结束回执', fail);
  assert(fee, '恢复后缺少费用回执', fail);
  return verdict('S7 worker 中断恢复', done, fail, { stuck, events, meter, fee });
}

// S4 预算封顶：10 分金额套餐加高功率，预算耗尽自动停机。
// 电费加服务费精确等于套餐价是预算封顶必须守住的不变量；偏差即 ErrBillingConflict。
async function s4() {
  const token = await login();
  const fail = [];
  await startCharge(token, 'amount');
  const o = await waitOrder(x => x?.status === 'charging', '订单转 charging', 90000);
  const id = o.id;
  await simEvent({ type: 'power', port: 1, power_deciwatts: 15000 });
  const done = await waitOrder(x => ['completed', 'refunded'].includes(x?.status), '预算封顶闭环', 480000);
  await waitJobDone(id);
  const cutoff = await orderCutoff(id);
  const job = await orderJob(id);
  const fee = await orderFee(id);
  const pay = await orderPayment(id);
  const reviews = await orderReviews(id);
  const meter = meterSummary((await orderEndReceipt(id))[0]?.[0]);
  assert(cutoff[0]?.[0] === 'budget_exhausted', `截止原因应 budget_exhausted，得到 ${writeJSON(cutoff)}`, fail);
  assert(Number(cutoff[0]?.[1]) + Number(cutoff[0]?.[2]) === 10, `电费加服务费应精确等于预算 10 分，得到 ${writeJSON(cutoff)}`, fail);
  assert(job[0]?.[0] === 'done', `计费应 done，得到 ${writeJSON(job)}`, fail);
  assert(reviews[0][0] === '0', '预算封顶不应产生人工复核', fail);
  assert(fee && Number(fee.total_cents) === 10, `费用回执应为 10 分，得到 ${writeJSON(fee)}`, fail);
  assert(Number(pay[0][1]) === 0, `预付等于实收无退款，得到 ${writeJSON(pay)}`, fail);
  assert(meter && meter.segments > 0 && meter.missingPower === 0, `分段应全部带功率，得到 ${writeJSON(meter)}`, fail);
  return verdict('S4 预算封顶', done, fail, { cutoff, job, fee, pay, meter });
}

// S8 断线重连：心跳中断不应被当作零功率；用户停机后应正常计费。
async function s8() {
  const token = await login();
  const fail = [];
  await startCharge(token, 'amount');
  const o = await waitOrder(x => x?.status === 'charging', '订单转 charging', 90000);
  const id = o.id;
  await sleep(45000);
  await call(USER, 'POST', '/user/charge/stop', { order_no: o.order_no }, token);
  const done = await waitOrder(x => x?.status === 'completed', '订单转 completed', 120000);
  await waitJobDone(id);
  const cutoff = await orderCutoff(id);
  const job = await orderJob(id);
  const fee = await orderFee(id);
  const refunds = await orderRefunds(id);
  assert(cutoff.length === 0 || cutoff[0]?.[0] === 'stop_request',
    `不得因断线误判零功率停机，得到 ${writeJSON(cutoff)}`, fail);
  assert(job[0]?.[0] === 'done', `计费应 done，得到 ${writeJSON(job)}`, fail);
  assert(fee && Number(fee.total_cents) >= 0, '缺少费用回执', fail);
  assert(refunds.length <= 1, `退款至多一条，得到 ${writeJSON(refunds)}`, fail);
  return verdict('S8 断线重连不误停', done, fail, { cutoff, job, fee, refunds });
}

const scenarios = { budget, s3, s1, s2, s5, s7, s4, s8 };
const run = scenarios[process.argv[2]];
if (!run) { console.error('用法: node scenarios.mjs <budget|s3|s1|s2|s5|s7|s4|s8>'); process.exit(2); }
await run();
