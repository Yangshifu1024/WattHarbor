const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const { createRequire } = require('node:module');
const { controllerPath, controllerSource } = require('./source.cjs');

// 真实控制器 + 可控制完成顺序的 POST；不访问后端，也不靠 sleep 推测交错。
function setup(kind, { autoConfirm = true } = {}) {
	const dialogs = [], navigations = [];
  const requests = [], app = { _generation: 0, globalData: { token: 'token' }, request(method, url, body) {
    if (method === 'POST') return new Promise((resolve, reject) => requests.push({ url, body, resolve, reject }));
    if (url === '/user/cards') return Promise.resolve({ items: [{ card_no: '123', status: 'active' }] });
    if (url === '/user/profile') return Promise.resolve({ phone_bound: false });
    if (url.endsWith('/history')) return Promise.resolve({ items: [] });
    return Promise.resolve({ order_no: 'C-1', status: 'completed', total_cents: 100, feedback_submitted: false });
  } };
  const paths = { invoice: 'pages/invoice/apply.js', phone: 'pages/profile/phone.js', cards: 'pages/wallet/cards.js', feedback: 'pages/charge/detail.js' };
  const filename = controllerPath(paths[kind]); let definition;
  vm.runInNewContext(controllerSource(filename), {
    getApp: () => app, Page: value => definition = value, require: createRequire(filename),
    wx: { showModal: options => { dialogs.push(options); if (autoConfirm) options.success?.({ confirm: true }); }, navigateBack() { navigations.push('back'); }, stopPullDownRefresh() {} },
  });
  const page = { ...definition, data: structuredClone(definition.data) }; let writes = 0;
  page.setData = patch => { writes++; Object.assign(page.data, patch); };
  page._generation = 0; page._session = 0; page._feedbackSession = 0; page._gone = false; page._orderId = 'C-1';
  function prepare() {
    if (kind === 'invoice') page.setData({ selected: { order_no: 'C-1' }, title: '测试用户' });
    if (kind === 'phone') page.setData({ phone: '13800000001' });
    if (kind === 'cards') page.setData({ cards: [{ card_no: '123', status: 'active' }] });
    if (kind === 'feedback') page.setData({ order: { order_no: 'C-1', status: 'completed' }, feedbackContent: '已完成充电' });
  }
  function submit() {
    if (kind === 'invoice') return page.submit();
    if (kind === 'phone') return page.bindDevelopment();
    if (kind === 'cards') return page.change({ currentTarget: { dataset: { card: '123', status: 'lost' } } });
    return page.submitFeedback();
  }
  const success = { invoice_no: 'INV-1', bound: true, unbound: true, submitted: true };
  return { page, app, requests, dialogs, navigations, prepare, submit, success, field: kind === 'invoice' ? 'submitting' : kind === 'feedback' ? 'feedbackBusy' : 'busy', get writes() { return writes; } };
}
async function started(f) {
  f.prepare(); const pending = f.submit(); await Promise.resolve();
  assert.equal(f.requests.length, 1);
  return { pending };
}
for (const kind of ['invoice', 'phone', 'cards', 'feedback']) {
  for (const outcome of ['success', 'failure']) test(`${kind}: 请求在隐藏时${outcome}后解除忙碌，返回可操作`, async () => {
    const f = setup(kind), { pending } = await started(f);
    f.page.onHide();
    if (outcome === 'success') f.requests[0].resolve(f.success); else f.requests[0].reject(Error('响应丢失'));
    await pending; assert.equal(f.page.data[f.field], false);
    await f.page.onShow(); assert.equal(f.page.data[f.field], false);
  });
  test(`${kind}: 返回页面时原请求未结束，不能重复提交`, async () => {
    const f = setup(kind), { pending } = await started(f);
    f.page.onHide(); await f.page.onShow(); f.prepare();
    await f.submit(); assert.equal(f.requests.length, 1); assert.equal(f.page.data[f.field], true);
    f.requests[0].reject(Error('响应丢失')); await pending;
    assert.equal(f.page.data[f.field], false);
  });
  test(`${kind}: 旧会话响应不能清理新会话正在执行的操作`, async () => {
    const f = setup(kind), { pending } = await started(f);
    f.page.onHide(); f.app._generation++; await f.page.onShow(); f.prepare();
    const next = f.submit(); await Promise.resolve(); assert.equal(f.requests.length, 2);
    const before = JSON.stringify({ notice: f.page.data.notice, error: f.page.data.error, bound: f.page.data.bound, feedbackSubmitted: f.page.data.feedbackSubmitted });
    f.requests[0].resolve(f.success); await pending;
    assert.equal(JSON.stringify({ notice: f.page.data.notice, error: f.page.data.error, bound: f.page.data.bound, feedbackSubmitted: f.page.data.feedbackSubmitted }), before);
    assert.equal(f.page.data[f.field], true);
    f.requests[1].reject(Error('B请求失败')); await next; assert.equal(f.page.data[f.field], false);
  });
  test(`${kind}: 卸载后 finally 不回写视图`, async () => {
    const f = setup(kind), { pending } = await started(f);
    f.page.onUnload(); const before = f.writes;
    f.requests[0].reject(Error('迟到错误')); await pending; assert.equal(f.writes, before);
  });
}
test('发票结果未知时沿用原请求内容及身份，不把编辑后的内容重用旧请求号', async () => {
  const f = setup('invoice'), { pending } = await started(f), original = JSON.stringify(f.requests[0].body);
  f.page.onHide(); f.requests[0].reject(Error('响应丢失')); await pending; await f.page.onShow();
  f.page.data.title = '后来编辑的抬头'; const retry = f.submit();
  assert.equal(JSON.stringify(f.requests[1].body), original);
  f.requests[1].resolve(f.success); await retry;
});

for (const action of ['wechat', 'unbind']) {
  test(`手机号${action}: 隐藏时完成释放锁，旧会话响应不回写新账号`, async () => {
    const f = setup('phone'); f.page.data.bound = true;
    const submit = () => action === 'wechat' ? f.page.onGetPhoneNumber({ detail: { code: 'code-A' } }) : f.page.unbind();
    const old = submit(); await Promise.resolve(); assert.equal(f.requests.length, 1);
    f.page.onHide(); f.app._generation++; await f.page.onShow();
    const privateState = () => JSON.stringify({ bound: f.page.data.bound, phone: f.page.data.phone, notice: f.page.data.notice, error: f.page.data.error, busy: f.page.data.busy });
    const before = privateState();
    f.requests[0].resolve(f.success); await old;
    assert.equal(privateState(), before);
    f.page.data.bound = true;
    const next = submit(); await Promise.resolve(); assert.equal(f.requests.length, 2);
    f.page.onHide(); f.requests[1].reject(Error('网络异常')); await next;
    assert.equal(f.page.data.busy, false);
  });
}
test('手机号解绑: A的确认框不能解绑后来登录的B，也不能清除B的提交锁', async () => {
  const f = setup('phone', { autoConfirm: false }); f.page.data.bound = true;
  const old = f.page.unbind(); assert.equal(f.page.data.busy, true);
  f.page.onHide(); f.app._generation++; await f.page.onShow();
  const next = f.page.onGetPhoneNumber({ detail: { code: 'code-B' } });
  assert.equal(f.requests.length, 1);
  f.dialogs[0].success({ confirm: true }); await old;
  assert.equal(f.requests.some(request => request.url === '/user/phone/unbind'), false);
  assert.equal(f.page.data.busy, true);
  f.requests[0].resolve(f.success); await next; assert.equal(f.page.data.busy, false);
});
for (const result of ['cancel', 'failure']) test(`手机号解绑: 确认框${result}释放提交锁`, async () => {
  const f = setup('phone', { autoConfirm: false }); f.page.data.bound = true;
  const pending = f.page.unbind();
  if (result === 'cancel') f.dialogs[0].success({ confirm: false }); else f.dialogs[0].fail(Error('弹窗失败'));
  await pending; assert.equal(f.requests.length, 0); assert.equal(f.page.data.busy, false);
});

for (const switchAccount of [false, true]) test(`发票成功确认框: 结束请求后导航仍核对所属账号 ${switchAccount}`, async () => {
  const f = setup('invoice', { autoConfirm: false }), { pending } = await started(f);
  f.requests[0].resolve(f.success); await pending;
  assert.equal(f.page.data.submitting, false); assert.equal(f.dialogs.length, 1);
  if (switchAccount) { f.page.onHide(); f.app._generation++; await f.page.onShow(); }
  f.dialogs[0].success({ confirm: true });
  assert.equal(f.navigations.length, switchAccount ? 0 : 1);
});
