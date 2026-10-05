const { controllerPath, controllerSource } = require('./source.cjs');
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');

// 首页横幅与支付前可用券：核对收起条件、只读语义和登录边界。
function page(relative, app, wx = {}) {
  let definition;
  const filename = controllerPath(relative);
  vm.runInNewContext(controllerSource(filename), {
    getApp: () => app, Page: value => { definition = value; }, require: createRequire(filename),
    wx: { stopPullDownRefresh() {}, navigateTo() {}, reLaunch() {}, getLocation: options => options.fail({}), ...wx },
  }, { filename });
  const instance = { ...definition, data: structuredClone(definition.data), _generation: 0, _gone: false };
  instance.setData = value => Object.assign(instance.data, value);
  return instance;
}

test('home raises the ongoing banner only when the server reports an order', async () => {
  const app = { globalData: { token: 't' }, request: async (_m, url) => {
    if (url.endsWith('/announcement/list')) return { items: [] };
    // 服务端无进行中订单时返回空串字段而不是 null。
    return { order_no: '', status: '' };
  } };
  const p = page('pages/index/index.js', app);
  await p.refresh();
  assert.equal(p.data.ongoing, null, '空 order_no 必须收起横幅');

  app.request = async (_m, url) => url.endsWith('/announcement/list')
    ? { items: [] } : { order_no: 'ORD:1', status: 'charging', device_id: 'DEV1', port_no: 2 };
  await p.refresh();
  assert.equal(p.data.ongoing.order_no, 'ORD:1');
  assert.equal(p.data.ongoing.statusLabel, '正在充电');
  assert.equal(p.data.ongoing.device_id, 'DEV1');
  p.goOngoing();
});

test('home labels startup confirmation without claiming the device started', async () => {
  const app = { globalData: { token: 't' }, request: async (_m, url) => url.endsWith('/announcement/list')
    ? { items: [] } : { order_no: 'ORD:2', status: 'paid' } };
  const p = page('pages/index/index.js', app);
  await p.refresh();
  assert.match(p.data.ongoing.statusLabel, /启动确认中/);
});

test('home hides the banner when signed out and keeps announcements anonymous', async () => {
  const calls = [];
  const app = { globalData: { token: '' }, request: async (_m, url, _d, auth) => {
    calls.push([url, auth]);
    return { items: [{ id: 3, title: '站点维护通知' }, { id: 4, title: '第二条' }] };
  } };
  const p = page('pages/index/index.js', app);
  await p.refresh();
  assert.equal(p.data.ongoing, null);
  assert.deepEqual(calls, [['/user/announcement/list', false]]);
  assert.equal(p.data.announcements.length, 2);
  // 控制器在 vm 上下文中构造对象，原型与本 realm 不同，按原始值比较。
  assert.deepEqual(p.data.announcements.map(item => [item.id, item.title]), [[3, '站点维护通知'], [4, '第二条']]);
});

test('home announcement strip is capped and titled from server data', async () => {
  const app = { globalData: { token: '' }, request: async () => ({ items: [
    { id: 1, title: '一' }, { id: 2, title: '二' }, { id: 3, title: '三' }, { id: 4, title: '四' }] }) };
  const p = page('pages/index/index.js', app);
  await p.refresh();
  assert.equal(p.data.announcements.length, 3, '横幅最多三条，超出部分由公告页查看');
});

test('available coupon list is read-only and never posts', async () => {
  let posts = 0;
  const app = { globalData: { token: 't' }, request: async (method, url) => {
    if (method === 'POST') { posts++; return {}; }
    assert.equal(url, '/user/coupons');
    return { items: [{ grant_id: '5', coupon_id: '2', name: '新客立减', discount_type: 'amount', min_charge_cents: 1000, expires_at: '2026-12-01T00:00:00Z' }] };
  } };
  const p = page('pages/coupons/available.js', app);
  await p.load();
  assert.equal(posts, 0, '后端未提供用户领取端点，本页不得发起任何写入');
  assert.equal(p.data.items[0].typeText, '立减券');
  assert.equal(p.data.items[0].minimumText, '10.00');
});

test('available coupon list requires login before requesting', async () => {
  let calls = 0;
  const app = { globalData: { token: '' }, request: async () => { calls++; return { items: [] }; } };
  const p = page('pages/coupons/available.js', app);
  await p.load();
  assert.equal(calls, 0);
  assert.equal(p.data.needsLogin, true);
});
