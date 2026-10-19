// pre-commit 钩子的外部行为只落在三件事上：跑了哪些命令、按什么顺序、失败时是否立刻中止。
// 用假的执行器替代 spawn，本文件不跑 go，也不改工作区。
import test from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { main } from './pre-commit.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url)).replace(/[\\/]$/, '');

// 记录每一步；failOn 命中的步骤返回给定退出码，其余通过。
function recorder(failOn = '', status = 1) {
  const steps = [];
  const lines = [];
  const run = step => {
    steps.push(step);
    return { status: step.label === failOn ? status : 0 };
  };
  return { steps, run, lines, log: line => lines.push(String(line)) };
}

test('钩子先跑规则层检查，再依次跑 go vet 与 go test', () => {
  const { steps, run, log, lines } = recorder();
  const status = main([], { run, log, error: log });
  assert.equal(status, 0);
  assert.deepEqual(lines, ['pre-commit ok: 规则层检查', 'pre-commit ok: go vet', 'pre-commit ok: go test']);
  assert.doesNotMatch(lines.join('\n'), /aborted/);
  assert.deepEqual(steps.map(step => step.label), ['规则层检查', 'go vet', 'go test']);
  // 规则层检查秒级完成且只读文档，排在最前面才能让文档漂移在跑 go 之前就被拦下。
  assert.equal(steps[0].command, process.execPath);
  assert.match(steps[0].args[0], /scripts[/\\]rules[/\\]check-rules\.mjs$/);
  assert.equal(steps[0].cwd, ROOT);
  assert.equal(steps[1].command, 'go');
  assert.deepEqual(steps[1].args, ['vet', './...']);
  assert.equal(steps[1].cwd, join(ROOT, 'backend'));
  assert.equal(steps[2].command, 'go');
  assert.deepEqual(steps[2].args, ['test', './...']);
  assert.equal(steps[2].cwd, join(ROOT, 'backend'));
});

test('规则层检查失败时立刻中止，不执行 go 命令', () => {
  const { steps, run, log, lines } = recorder('规则层检查');
  const status = main([], { run, log, error: log });
  assert.equal(status, 1);
  assert.deepEqual(steps.map(step => step.label), ['规则层检查']);
  assert.match(lines.join('\n'), /pre-commit aborted: 规则层检查 failed/);
});

test('go vet 失败时中止，不再跑 go test', () => {
  const { steps, run, log, lines } = recorder('go vet');
  const status = main([], { run, log, error: log });
  assert.equal(status, 1);
  assert.deepEqual(steps.map(step => step.label), ['规则层检查', 'go vet']);
  assert.match(lines.join('\n'), /pre-commit aborted: go vet failed/);
});

test('钩子拒绝多余参数', () => {
  const { steps, run, log, lines } = recorder();
  const status = main(['--force'], { run, log, error: log });
  assert.equal(status, 2);
  assert.equal(steps.length, 0);
  assert.match(lines.join('\n'), /用法/);
});
