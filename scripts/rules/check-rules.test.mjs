// 规则层检查的外部行为：判定结果只落在退出码与输出文本上，不绑定脚本内部结构。
// 本文件不连数据库、不连网络、不启动服务。
import test from 'node:test';
import assert from 'node:assert/strict';
import { main } from './check-rules.mjs';

// 记录 main 的全部输出，便于断言报告内容；成功与失败走同一个收集器。
function capture() {
  const lines = [];
  return { lines, options: { log: line => lines.push(String(line)), error: line => lines.push(String(line)) } };
}

test('尚未实现的断言逐条标为尚未实现，不计入已验证', () => {
  const { lines, options } = capture();
  const status = main([], options);
  const output = lines.join('\n');
  assert.equal(status, 0);
  assert.equal(lines.filter(line => line.startsWith('尚未实现：')).length, 6);
  assert.match(output, /0 条失败，6 条尚未实现/);
  assert.doesNotMatch(output, /全部通过/);
});

test('某条不变量被违反时报红，并点名不变量与涉及文件', () => {
  const { lines, options } = capture();
  const violated = [{
    id: 'enum-value-strings',
    title: '数据库枚举的取值串不得出现在任何文档中，只能是指向迁移 schema 的指针',
    assert: () => [{ file: 'docs/business-contracts.md', message: '文档重述了 payment_status 的取值' }],
  }];
  const status = main([], { ...options, invariants: violated });
  const output = lines.join('\n');
  assert.notEqual(status, 0);
  assert.match(output, /数据库枚举的取值串不得出现在任何文档中/);
  assert.match(output, /enum-value-strings/);
  assert.match(output, /docs\/business-contracts\.md/);
});
