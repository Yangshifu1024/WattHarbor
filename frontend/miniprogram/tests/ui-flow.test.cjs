const { controllerPath, controllerSource } = require('./source.cjs');
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const { createRequire } = require('node:module');

function page(relative, app, overrides = {}) {
 const filename = controllerPath(relative);
 let definition;
 const routes = [];
 vm.runInNewContext(controllerSource(filename), { getApp: () => app, Page: p => definition = p,
  require: createRequire(filename), setTimeout: () => 1, clearTimeout() {},
  wx: { navigateTo: options => { routes.push(options.url); options.success?.(); }, stopPullDownRefresh() {}, ...overrides }
 }, { filename });
 const p = { ...definition, data: structuredClone(definition.data) };
 p.setData = patch => Object.assign(p.data, patch);
 return { p, routes };
}
const port = { port_id: 'DEV001:3', port_code: 'DEV001:3', device_id: 'DEV001', port_no: 3, port_status: 'idle', device_status: 'enabled', online: true, available: true };
const offers = [{ id: 1, name: '预付 5 元', mode: 'amount', price_cents: 500, max_minutes: 480 }, { id: 2, name: '2 小时', mode: 'duration', price_cents: 300, duration_minutes: 120 }, { id: 3, name: '1 度', mode: 'energy', price_cents: 200, energy_wh: 1000 }];
async function scanned(items = offers) {
 const app = { globalData: { token: '' }, request: async (_method, url) => url.endsWith('/resolve') ? { kind: 'port', device_id: 'DEV001', device_status: 'enabled', port } : { port_id: port.port_id, items } };
 const { p } = page('pages/scan-result/scan-result.js', app);
 p.onLoad({ code: 'DEV001:3' });
 await p.onShow();
 return p;
}
const choose = id => ({ currentTarget: { dataset: { id } } });
const mode = value => ({ currentTarget: { dataset: { mode: value } } });

test('mode changes clear old selections and reject offers from another mode', async () => {
 const p = await scanned();
 assert.equal(p.data.offerMode, 'amount');
 p.selectOffer(choose(1)); assert.equal(p.data.selectedOffer.id, 1);
 p.selectMode(mode('duration')); assert.equal(p.data.selectedOffer, null);
 p.selectOffer(choose(1)); assert.equal(p.data.selectedOffer, null);
 p.selectOffer(choose(2)); assert.equal(p.data.selectedOffer.id, 2);
 p.selectMode(mode('energy')); assert.equal(p.data.selectedOffer, null);
 p.selectOffer(choose(3)); assert.equal(p.data.selectedOffer.id, 3);
});
test('empty mode cannot pay and mode switching cannot mutate a started operation', async () => {
 const p = await scanned([offers[1]]);
 assert.equal(p.data.offerMode, 'duration');
 p.selectMode(mode('amount')); await p.pay();
 assert.match(p.data.error, /选择充电方案/);
 p.selectMode(mode('duration')); p.selectOffer(choose(2));
 p._requestID = 'original'; p.setData({ startAttempted: true });
 p.selectMode(mode('energy')); p.selectOffer(choose(3));
 assert.equal(p.data.offerMode, 'duration'); assert.equal(p.data.selectedOffer.id, 2); assert.equal(p._requestID, 'original');
});
test('selecting another port invalidates prior package and chooses an available mode', async () => {
 const p = await scanned(); p.selectOffer(choose(1));
 p.data.ports.push({ ...port, port_id: 'DEV001:4', port_no: 4, selectable: true });
 // 新建测试实例并替换请求方法，模拟另一种端口响应。
 const app = { globalData: { token: '' }, request: async (_method, url) => url.endsWith('/port') ? { ...port, port_id: 'DEV001:4', port_no: 4 } : { port_id: 'DEV001:4', items: [offers[2]] } };
 const { p: other } = page('pages/scan-result/scan-result.js', app);
 other.onLoad({ code: 'DEV001' }); other.setData({ deviceId: 'DEV001', selected: port, selectedOffer: offers[0], ports: p.data.ports });
 await other.selectPort(choose('DEV001:4'));
 assert.equal(other.data.selectedOffer, null); assert.equal(other.data.offerMode, 'energy');
});
test('station device selection reaches scan resolver only for an online device', async () => {
 const app = { request: async () => ({ name: '站点', devices: [{ device_id: 'DEV001', online: true }, { device_id: 'DEV002', online: false }, { device_id: 'DEV003', runtime_available: false }] }) };
 const { p, routes } = page('pages/stations/detail.js', app);
 p.onLoad({ id: '1' }); await p.load();
 p.openDevice(choose('DEV002')); p.openDevice(choose('DEV003')); assert.equal(routes.length, 0);
 p.openDevice(choose('DEV001')); assert.equal(routes[0], '/pages/scan-result/scan-result?code=DEV001');
});
test('location refusal still loads public stations without fake coordinates', async () => {
 const calls = [];
 const app = { request: async (...args) => { calls.push(args); return { items: [{ id: 1, name: '站点', distance_km: null }] }; } };
 const { p } = page('pages/stations/nearby.js', app, { getLocation: options => options.fail() });
 p.onLoad(); await p.onShow();
 assert.equal(Object.keys(calls[0][2]).length, 0); assert.equal(calls[0][3], false);
 assert.equal(p.data.items[0].distanceText, '距离未知'); assert.match(p.data.locationNotice, /最新电站/);
});
test('late location callbacks cannot mutate a hidden page or start a query', async () => {
 let callback; let calls = 0;
 const { p } = page('pages/stations/nearby.js', { request: async () => { calls++; return { items: [] }; } }, { getLocation: options => callback = options });
 p.onLoad(); const loading = p.onShow(); p.onHide(); callback.success({ latitude: 10, longitude: 20 }); await loading;
 assert.equal(calls, 0); assert.equal(p.data.latitude, null);
});
test('report scanner resolves a device before opening its fault form', async () => {
 const { p, routes } = page('pages/scan/scan.js', { request: async (method, url, body, auth) => {
  assert.equal(url, '/user/scan/resolve'); assert.equal(auth, false); return { device_id: 'DEV001' };
 } });
 p.onLoad({ report_fault: '1' }); await p.openResult('DEV001:3');
 assert.equal(routes[0], '/pages/dev/fault?device_id=DEV001');
});
test('wallet ledger failure preserves balance and late ledger cannot cross lifecycle', async () => {
 let fail = true, resolve;
 const app = { _generation: 0, globalData: { token: 't' }, request: async (_m, url) => {
  if (url.endsWith('/balance')) return { available_cents: 1200, frozen_cents: 100 };
  if (fail) throw Error('流水暂不可用');
  return new Promise(r => resolve = r);
 } };
 const { p } = page('pages/wallet/wallet.js', app); p._generation = 0;
 await p.load(); assert.equal(p.data.wallet.availableText, '12.00'); assert.equal(p.data.error, ''); assert.equal(p.data.txnsError, '流水暂不可用');
 fail = false; const request = p.loadTxns(); p.onHide(); resolve({ items: [{ txn_no: '1', amount_cents: 100 }] }); await request;
 assert.equal(p.data.txns.length, 0);
});
test('power samples retain real zero and break missing values instead of fabricating a trace', () => {
 const { powerSegments } = require('../src/utils/curve');
 const groups = powerSegments([{ ts: '2026-10-08T00:00:00Z', power_w: 0 }, { ts: '2026-10-08T00:00:10Z', power_w: null }, { ts: '2026-10-08T00:00:20Z', power_w: '120' }, { ts: 'bad', power_w: 20 }]);
 assert.equal(groups.length, 2); assert.equal(groups[0][0].value, 0); assert.equal(groups[1][0].value, 120);
});
test('automatic curve reads stop at page hide and do not suppress a trusted snapshot on failure', async () => {
 let fail = true, resolve;
 const app = { globalData: { token: 't' }, request: async (_method, url) => {
  if (url.endsWith('/snapshot')) return { status: 'charging', order_no: 'O1', display: { show_power: true }, poll_continue: true };
  if (fail) throw Error('曲线暂不可用');
  return new Promise(r => resolve = r);
 } };
 const { p } = page('pages/charge/charging.js', app); p.onLoad({ order_no: 'O1' });
 await p.onShow(); assert.equal(p.data.snapshot.status, 'charging'); assert.equal(p.data.error, ''); assert.equal(p.data.curveError, '曲线暂不可用');
 fail = false; const request = p.loadCurve(); p.onHide(); resolve({ series: [{ ts: '2026-10-08T00:00:00Z', power_w: 1 }] }); await request;
 assert.equal(p.data.curve, null); assert.equal(p.data.curveLoading, false);
});

test('payment recovery uses an exact payment match instead of treating merchant number as charge number', async () => {
 const { findPaymentOrder } = require('../src/utils/payment-order');
 const app = { request: async (_method, url) => {
  if (url.endsWith('/ongoing')) return { order_no: 'C-other', device_id: 'DEV001', port_no: 1 };
  if (url.endsWith('/history')) return { items: [{ order_no: 'C-paid', device_id: 'DEV001', port_no: 1 }] };
  return { order_no: url.endsWith('/C-other') ? 'C-other' : 'C-paid', payment: { order_no: url.endsWith('/C-other') ? 'P-other' : 'P-paid' } };
 } };
 assert.equal(await findPaymentOrder(app, 'P-paid', 'DEV001', 1, () => true), 'C-paid');
 assert.equal(await findPaymentOrder(app, 'P-missing', 'DEV001', 1, () => true), null);
});
test('payment without a created order shows confirmation and retries instead of querying merchant as order', async () => {
 let orderReady = false; const snapshotQueries = [];
 const app = { _generation: 0, globalData: { token: 't' }, request: async (_method, url, body) => {
  if (url.endsWith('/ongoing')) return orderReady ? { order_no: 'C1', device_id: 'DEV001', port_no: 1 } : { order_no: '' };
  if (url.endsWith('/history')) return { items: [] };
  if (url.endsWith('/C1')) return { order_no: 'C1', payment: { order_no: 'P1' } };
  snapshotQueries.push(body.order_id); return { order_no: 'C1', status: 'paid', poll_continue: true };
 } };
 const { p } = page('pages/charge/charging.js', app); p.onLoad({ payment_no: 'P1', device_id: 'DEV001', port_no: '1' });
 await p.onShow(); assert.equal(p.data.waitingOrder, true); assert.equal(snapshotQueries.length, 0);
 orderReady = true; await p.load(); assert.equal(p.data.waitingOrder, false); assert.equal(snapshotQueries[0], 'C1'); assert.equal(p.data.snapshot.statusLabel, '启动确认中');
});
