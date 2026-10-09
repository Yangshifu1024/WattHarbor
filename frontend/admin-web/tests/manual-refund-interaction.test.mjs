import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

// 执行真实组件的事件处理器；轻量 hook/JSX 宿主只保存状态、依赖与卸载清理，不模拟退款业务。
// 这覆盖组件逻辑，不替代浏览器布局、键盘或真实支付验收。
const compiled = ts.transpileModule(readFileSync(new URL('../src/pages/ManualRefund.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
function harness(storage, requests, props) {
  const hooks = [], effects = []; let cursor = 0, writes = 0, tree;
  const react = {
    useState(initial) { const i = cursor++; hooks[i] ??= { value: initial }; return [hooks[i].value, value => { writes++; hooks[i].value = value; }]; },
    useRef(initial) { const i = cursor++; return hooks[i] ??= { current: initial }; },
    useEffect(fn, deps) { const i = cursor++; if (!hooks[i] || deps.some((v, n) => v !== hooks[i].deps[n])) { hooks[i]?.cleanup?.(); hooks[i] = { deps }; effects.push(() => { hooks[i].cleanup = fn(); }); } },
  };
  const components = { Alert: 'Alert', Button: 'Button', Input: Object.assign(() => {}, { TextArea: 'TextArea' }), Modal: 'Modal' };
  const module = { exports: {} };
  vm.runInNewContext(compiled, {
    module, exports: module.exports, crypto: { randomUUID: () => 'request-' + requests.length }, localStorage: storage,
    require(name) {
      if (name === 'react') return react;
      if (name === 'antd') return components;
      if (name === 'axios') return { default: { isAxiosError: value => !!value?.response } };
      if (name === 'react/jsx-runtime') return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }), Fragment: 'Fragment' };
      if (name === '../api/client') return { apiPost: (path, body) => new Promise((resolve, reject) => requests.push({ path, body, resolve, reject })) };
      throw Error(name);
    },
  });
  function render() { cursor = 0; tree = module.exports.default(props); effects.splice(0).forEach(fn => fn()); }
  function find(type) {
    function walk(node) { if (!node || typeof node !== 'object') return null; if (Array.isArray(node)) return node.map(walk).find(Boolean); return node.type === type ? node : walk(node.props?.children); }
    return walk(tree);
  }
  render();
  return {
    render, find, get writes() { return writes; },
    unmount() { hooks.forEach(h => h?.cleanup?.()); },
    show() { find('Button').props.onClick(); render(); },
    prepare() { this.show(); find(components.Input).props.onChange({ target: { value: '12.34' } }); find('TextArea').props.onChange({ target: { value: '已核实退款依据' } }); render(); },
    submit() { const pending = find('Modal').props.onOk(); render(); return pending; },
  };
}
function setup() {
  const saved = new Map([['cp_session_epoch', 'session-a']]), requests = []; let created = 0;
  const storage = { getItem: key => saved.get(key) ?? null, setItem: (key, v) => saved.set(key, v), removeItem: key => saved.delete(key) };
  const component = actorId => harness(storage, requests, { orderId: 123, orderNo: 'C-123', actorId, onCreated: () => created++ });
  return { saved, requests, component, get created() { return created; } };
}
test('两管理员各自恢复草稿，网络失败重试复用金额、理由和请求标识', async () => {
  const f = setup(), a = f.component('9007199254740993'); a.prepare();
  const p = a.submit(); const first = f.requests[0]; first.reject(Error('网络中断')); await p; a.render();
  assert.equal(a.find('Modal').props.okText, '重试同一申请');
  const retry = a.submit(); assert.deepEqual(f.requests[1].body, first.body);
  f.requests[1].reject(Error('响应丢失')); await retry; a.unmount();
  const b = f.component('9007199254740995'); b.show(); assert.equal(b.find('Modal').props.okText, '提交退款申请'); b.unmount();
  const restored = f.component('9007199254740993'); restored.show(); assert.equal(restored.find('Modal').props.okText, '重试同一申请'); restored.unmount();
  assert.equal(f.created, 0); assert.ok(f.saved.has('cp_manual_refund_9007199254740993_123'));
});
for (const result of ['success', '400', '409', '422', 'network']) {
  test(`会话切换后旧 ${result} 响应不删草稿、不回写状态、不通知创建`, async () => {
    const f = setup(), a = f.component('a'); a.prepare(); const p = a.submit(), before = a.writes;
    f.saved.set('cp_session_epoch', 'session-b');
    const r = f.requests[0];
    if (result === 'success') r.resolve({ refund_no: 'R-1', request_id: r.body.request_id, created: true });
    else r.reject(result === 'network' ? Error('网络中断') : { response: { status: Number(result) } });
    await p; assert.equal(a.writes, before); assert.ok(f.saved.has('cp_manual_refund_a_123')); assert.equal(f.created, 0); a.unmount();
  });
}
test('组件卸载后相同 epoch 的旧错误和 finally 不回写', async () => {
  const f = setup(), a = f.component('a'); a.prepare(); const p = a.submit(), before = a.writes; a.unmount();
  f.requests[0].reject({ response: { status: 400 } }); await p;
  assert.equal(a.writes, before); assert.ok(f.saved.has('cp_manual_refund_a_123'));
});
test('当前会话成功确认后才清理申请并通知刷新', async () => {
  const f = setup(), a = f.component('a'); a.prepare(); const p = a.submit(), r = f.requests[0];
  r.resolve({ refund_no: 'R-1', request_id: r.body.request_id, created: true }); await p; a.render();
  assert.equal(f.created, 1); assert.equal(f.saved.has('cp_manual_refund_a_123'), false); assert.equal(a.find('Modal').props.open, false); a.unmount();
});
