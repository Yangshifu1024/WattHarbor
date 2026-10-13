import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import dayjs from 'dayjs';

const compiled = ts.transpileModule(readFileSync(new URL('../src/pages/Exports.tsx', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText;
// 执行真实表单 validator 和 create 事件，轻量 JSX 宿主不复制导出提交逻辑。
function harness(values) {
  let cursor = 0, tree; const hooks = [], effects = [], requests = [];
  const react = {
    useState(initial) { const i = cursor++; hooks[i] ??= { value: initial }; return [hooks[i].value, value => { hooks[i].value = value; }]; },
    useCallback(fn) { return fn; },
    useEffect(fn) { cursor++; effects.push(fn); },
  };
  const form = { async validateFields() {
    if (values.resource === 'bills') {
      const field = find('Form.Item', props => props.name === 'period');
      assert.ok(field, '账单日期输入必须出现在实际表单');
      try { await field.props.rules.find(rule => rule.validator).validator(null, values.period); }
      catch { throw { errorFields: [{ name: 'period' }] }; }
    }
    return values;
  } };
  const components = {
    Alert: 'Alert', Button: 'Button', Modal: 'Modal', Select: 'Select', Space: 'Space', Table: 'Table', Tag: 'Tag',
    App: { useApp: () => ({ message: { success() {}, warning() {}, error() {} } }) },
    Form: Object.assign(() => {}, { Item: 'Form.Item', useForm: () => [form], useWatch: () => values.resource }),
    DatePicker: { RangePicker: 'RangePicker' }, Typography: { Text: 'Text', Paragraph: 'Paragraph' },
  };
  const module = { exports: {} };
  vm.runInNewContext(compiled, {
    module, exports: module.exports, crypto: { randomUUID: () => '11111111-1111-4111-8111-111111111111' },
    localStorage: { getItem: () => JSON.stringify({ permissions: ['export.create'] }) },
    require(name) {
      if (name === 'react') return react;
      if (name === 'antd') return components;
      if (name === '@ant-design/icons') return {};
      if (name === 'react/jsx-runtime') return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) };
      if (name === '../api/client') return {
        apiGet: async path => path.endsWith('/resources') ? { items: ['bills', 'devices'], max_rows: 100 } : { items: [], total: 0 },
        apiPost: async (path, body) => { requests.push({ path, body }); return { status: 'completed', row_count: 1 }; },
      };
      if (name === '../utils/time') return { formatTime: value => value };
      if (name === '../components/LoadError') return { LoadError: 'LoadError' };
      if (name === '../utils/tablePagination') return { useTablePagination: () => ({ pagination: { page: 1, page_size: 20 }, tablePagination: {} }) };
      throw Error(name);
    },
  });
  function render() { cursor = 0; tree = module.exports.default(); effects.splice(0).forEach(fn => fn()); }
  function find(type, match = () => true) {
    function walk(node) {
      if (!node || typeof node !== 'object') return null;
      if (Array.isArray(node)) return node.map(walk).find(Boolean);
      if (node.type === type && match(node.props)) return node;
      if (typeof node.type === 'function' && node.type !== components.Form) return walk(node.type(node.props));
      return walk(node.props?.children);
    }
    return walk(tree);
  }
  render();
  return { render, find, requests, async submit() { await find('Modal').props.onOk(); } };
}
for (const [from, to] of [['2026-10-01', '2026-10-01'], ['2026-10-01', '2026-10-31']]) {
  test(`账单 ${from} 至 ${to} 提交准确日期`, async () => {
    const f = harness({ resource: 'bills', period: [dayjs(from), dayjs(to)] }); await f.submit();
    assert.equal(f.requests.length, 1);
    assert.deepEqual(JSON.parse(JSON.stringify(f.requests[0].body.filter)), { from, to });
  });
}
for (const period of [undefined, [dayjs('2026-10-01')], [null, dayjs('2026-10-01')], [dayjs('2026-10-01'), dayjs('2026-11-01')], [dayjs('2026-10-02'), dayjs('2026-10-01')]]) {
  test(`无效账单日期不发送导出请求 ${period?.map(value => value?.format('YYYY-MM-DD') || 'missing').join('/') || 'missing'}`, async () => {
    const f = harness({ resource: 'bills', period }); await f.submit(); assert.equal(f.requests.length, 0);
  });
}
test('从账单切到设备导出，不提交旧日期筛选', async () => {
  const values = { resource: 'bills', period: [dayjs('2026-10-01'), dayjs('2026-10-31')] }, f = harness(values);
  values.resource = 'devices'; f.render(); await f.submit();
  assert.equal(f.requests.length, 1); assert.equal(f.requests[0].body.resource, 'devices');
  assert.deepEqual(JSON.parse(JSON.stringify(f.requests[0].body.filter)), {});
});
