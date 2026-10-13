const { controllerPath, controllerSource } = require('./source.cjs');
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');

// 结算账单：新接入的列表、详情与已读凭据，核对分页、归属语义与读取/已读两个独立事实。
function page(name, app, wx = {}) {
  let definition;
  const filename = controllerPath(`pages/bills/${name}.js`);
  vm.runInNewContext(controllerSource(filename), {
    getApp: () => app, Page: value => { definition = value; }, require: createRequire(filename),
    wx: { stopPullDownRefresh() {}, navigateTo() {}, ...wx },
  }, { filename });
  const instance = { ...definition, data: structuredClone(definition.data), _generation: 0, _gone: false };
  instance.setData = value => Object.assign(instance.data, value);
  return instance;
}
const bill = (id, read) => ({ id: String(id), bill_no: 'BILL_' + id, order_no: 'ORD_' + id, device_id: 'DEV1', total_cents: 1234, electric_cents: 1000, service_cents: 234, issued_at: '2026-10-01T02:00:00Z', read_at: read ? '2026-10-02T02:00:00Z' : null });

test('bill list paginates, keeps prior rows and marks unread from read_at', async () => {
  const calls = [];
  const app = { globalData: { token: 't' }, request: async (_m, url, query) => {
    calls.push([url, query.page]);
    return { items: [bill(query.page, false)], total: 2 };
  } };
  const p = page('list', app);
  await p.load(true); await p.load(false);
  assert.deepEqual(calls, [['/user/bills', 1], ['/user/bills', 2]]);
  assert.equal(p.data.items.length, 2);
  assert.equal(p.data.items[0].unread, true);
  assert.equal(p.data.items[0].totalText, '¥12.34');
  assert.equal(p.data.items[0].electricText, '¥10.00');
});

test('bill list does not mark read and requires login before reading any row', async () => {
  let posts = 0;
  const app = { globalData: { token: 't' }, request: async (method, url) => {
    if (method === 'POST') { posts++; return { read: true }; }
    return { items: [bill(1, false)], total: 1 };
  } };
  await page('list', app).load(true);
  assert.equal(posts, 0, '列表页不得代替详情页写已读凭据');

  const anonymous = { globalData: { token: '' }, request: async () => { throw new Error('不应发起请求'); } };
  const p = page('list', anonymous);
  await p.load(true);
  assert.equal(p.data.needsLogin, true);
  assert.equal(p.data.items.length, 0);
});

test('bill detail marks read once after the bill is displayed', async () => {
  const calls = [];
  const app = { globalData: { token: 't' }, request: async (method, url) => {
    calls.push([method, url]);
    if (method === 'POST') return { id: '9', read: true };
    return bill(9, false);
  } };
  const p = page('detail', app);
  p.onLoad({ id: '9' });
  await p.load(); await p.load();
  const gets = calls.filter(c => c[0] === 'GET');
  assert.ok(gets.length >= 1);
  assert.ok(gets.every(c => c[1] === '/user/bills/9'), '详情只读取路径上的这张账单');
  assert.equal(calls.filter(c => c[0] === 'POST').length, 1, '同一账单只写一次已读凭据');
  assert.ok(calls.some(c => c[0] === 'POST' && c[1] === '/user/bills/9/read'));
  assert.equal(p.data.bill.unread, false);
  assert.equal(p.data.readNotice, '已标记为已读');
});

test('bill detail keeps content when the read receipt fails with 503', async () => {
  const app = { globalData: { token: 't' }, request: async (method) => {
    if (method === 'POST') { const error = new Error('账单状态暂时无法保存'); error.status = 503; throw error; }
    return bill(9, false);
  } };
  const p = page('detail', app);
  p.onLoad({ id: '9' });
  await p.load();
  assert.equal(p.data.bill.totalText, '¥12.34', '已读保存失败不得撤销已展示的账单');
  assert.match(p.data.readError, /暂时无法保存/);
  assert.equal(p.data.error, '');
});

test('bill detail refuses an out-of-range identifier without requesting', async () => {
  let calls = 0;
  const app = { globalData: { token: 't' }, request: async () => { calls++; return bill(1, true); } };
  const p = page('detail', app);
  p.onLoad({ id: 'abc' });
  await p.load();
  assert.equal(calls, 0);
  assert.equal(p.data.error, '缺少账单标识');
  assert.equal(p.data.bill, null);
});

test('bill detail retries the read receipt after a failure and clears the error once saved', async () => {
  let posts = 0;
  const app = { globalData: { token: 't' }, request: async (method) => {
    if (method === 'POST') { posts++; if (posts === 1) { const error = new Error('账单状态暂时无法保存'); error.status = 503; throw error; } return { read: true }; }
    return bill(9, false);
  } };
  const p = page('detail', app);
  p.onLoad({ id: '9' });
  await p.load();
  assert.match(p.data.readError, /暂时无法保存/);
  await p.load();
  assert.equal(posts, 2, '保存失败后必须允许重试同一张账单');
  assert.equal(p.data.bill.unread, false);
  assert.equal(p.data.readError, '');
  assert.equal(p.data.readNotice, '已标记为已读');
});

test('bill detail surfaces the 404 contract instead of an empty bill', async () => {
  const app = { globalData: { token: 't' }, request: async (_m, url) => {
    if (url.endsWith('/read')) return { read: true };
    const error = new Error('账单不存在'); error.status = 404; throw error;
  } };
  const p = page('detail', app);
  p.onLoad({ id: '404404404404404404' });
  await p.load();
  assert.equal(p.data.bill, null);
  assert.equal(p.data.error, '账单不存在');
});

test('server read flag preserves a saved receipt without inventing a read timestamp', async () => {
 const app={globalData:{token:'t'},request:async()=>({items:[{...bill('8996759476100805168',false),read:true}],total:1})};
 const p=page('list',app);await p.load(true);assert.equal(p.data.items[0].unread,false);assert.equal(p.data.items[0].readText,'');
});

test('账单来源跳转使用业务订单号，不使用账单或支付编号', async () => {
  const routes = [];
  const p = page('detail', { globalData: { token: 't' }, request: async () => ({ ...bill(9, true), order_no: 'C/SOURCE 1', payment_order_id: 77 }) }, { navigateTo: ({ url }) => routes.push(url) });
  p._id = '9'; await p.load(); p.orderDetail();
  assert.deepEqual(routes, ['/pages/charge/detail?order_id=C%2FSOURCE%201']);
  delete p.data.bill.order_no; p.orderDetail(); assert.equal(routes.length, 1);
});
