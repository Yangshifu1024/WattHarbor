// 检查任务顺序、失败退出和跨平台路径；不启动服务或连接数据库。
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { main } from '../tasks.mjs';

// fixture 创建临时仓库目录，用替代执行器记录命令；测试结束后删除目录。
// 可覆盖执行器、环境和平台来模拟失败。
function fixture(t, overrides = {}) {
  const root = mkdtempSync(join(tmpdir(), 'wattharbor-tasks-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const calls = [];
  const options = {
    root, env: {}, platform: 'linux', log() {}, error() {},
    run(command, args, options) { calls.push({ command, args, options }); return { status: 0 }; },
    ...overrides,
  };
  return { root, calls, options };
}

test('check 以规则检查开头，再顺序执行格式化、静态检查与测试', t => {
  const f = fixture(t);
  assert.equal(main(['check'], f.options), 0);
  assert.deepEqual(f.calls.map(c => c.args), [
    [join(f.root, 'scripts/rules/check-rules.mjs')],
    ['fmt', './...'], ['vet', './...'], ['test', './...'],
  ]);
  assert.equal(f.calls[0].command, process.execPath);
  assert.equal(f.calls[0].options.cwd, f.root);
  for (const c of f.calls.slice(1)) {
    assert.equal(c.options.cwd, join(f.root, 'backend'));
    assert.equal(c.options.shell, false);
  }
});

test('规则检查不通过时 check 立刻返回，不再执行后续检查', t => {
  const f = fixture(t);
  f.options.run = (...args) => { f.calls.push(args); return { status: f.calls.length === 1 ? 1 : 0 }; };
  assert.equal(main(['check'], f.options), 1);
  assert.equal(f.calls.length, 1);
});

test('check-rules 作为独立任务调用仓库内的规则检查脚本', t => {
  const f = fixture(t);
  assert.equal(main(['check-rules'], f.options), 0);
  assert.equal(f.calls[0].command, process.execPath);
  assert.deepEqual(f.calls[0].args, [join(f.root, 'scripts/rules/check-rules.mjs')]);
  assert.equal(f.calls[0].options.cwd, f.root);
});

test('check stops at the first failed child and preserves its exit code', t => {
  const f = fixture(t);
  f.options.run = (...args) => { f.calls.push(args); return { status: f.calls.length === 2 ? 7 : 0 }; };
  assert.equal(main(['check'], f.options), 7);
  assert.equal(f.calls.length, 2);
});

test('invalid tasks, extra arguments and invalid services have no side effects', t => {
  const f = fixture(t);
  for (const args of [['unknown'], ['check', 'extra'], ['build'], ['build', '../escape'], ['build', 'user', 'extra']]) {
    assert.equal(main(args, f.options), 2);
  }
  assert.equal(f.calls.length, 0);
  assert.equal(existsSync(join(f.root, 'bin')), false);
});

test('integration refuses an absent or blank database URL before launching Go', t => {
  for (const url of [undefined, '', '  ']) {
    const f = fixture(t, { env: { TEST_MYSQL_URL: url } });
    assert.equal(main(['test-integration'], f.options), 2);
    assert.equal(f.calls.length, 0);
  }
});

test('integration forwards both database and Redis environment with integration tag', t => {
  const env = { TEST_MYSQL_URL: 'mysql://localhost/central_db', TEST_REDIS_URL: 'redis://localhost/0' };
  const f = fixture(t, { env });
  assert.equal(main(['test-integration'], f.options), 0);
  assert.deepEqual(f.calls[0].args, ['test', '-tags=integration', '-count=1', './...']);
  assert.equal(f.calls[0].options.env, env);
});

test('Windows build creates bin and explicitly uses .exe output', t => {
  const f = fixture(t, { platform: 'win32' });
  assert.equal(main(['build', 'central'], f.options), 0);
  assert.equal(existsSync(join(f.root, 'bin')), true);
  assert.equal(f.calls[0].command, 'go.exe');
  assert.deepEqual(f.calls[0].args, ['build', '-trimpath', '-o', join(f.root, 'bin/central.exe'), './cmd/central']);
});

test('build accepts SERVICE and positional service takes priority', t => {
  const f = fixture(t, { env: { SERVICE: 'worker' } });
  assert.equal(main(['build'], f.options), 0);
  assert.equal(f.calls[0].args.at(-1), './cmd/worker');
  assert.equal(main(['build', 'admin'], f.options), 0);
  assert.equal(f.calls[1].args.at(-1), './cmd/admin');
});

test('build-all builds all deployment units into the root bin directory', t => {
  const f = fixture(t);
  assert.equal(main(['build-all'], f.options), 0);
  assert.deepEqual(f.calls[0].args, ['build', '-trimpath', '-o', `${join(f.root, 'bin')}/`, './cmd/...']);
});

test('clean removes only bin and succeeds when bin is already absent', t => {
  const f = fixture(t);
  for (const name of ['bin', '.tmp']) { mkdirSync(join(f.root, name)); writeFileSync(join(f.root, name, 'keep'), 'fixture'); }
  assert.equal(main(['clean'], f.options), 0);
  assert.equal(existsSync(join(f.root, 'bin')), false);
  assert.equal(existsSync(join(f.root, '.tmp/keep')), true);
  assert.equal(main(['clean'], f.options), 0);
});

test('development tasks preserve the existing start script and stop semantics', t => {
  const f = fixture(t, { platform: 'win32' });
  assert.equal(main(['dev-up'], f.options), 0);
  assert.equal(f.calls[0].command, process.execPath);
  assert.deepEqual(f.calls[0].args, [join(f.root, 'scripts/dev/start.mjs')]);
  assert.equal(main(['dev-down'], f.options), 0);
  assert.equal(f.calls[1].command, 'docker.exe');
  assert.deepEqual(f.calls[1].args, ['compose', '-f', join(f.root, 'docker/compose.dev.yaml'), 'stop']);
});

test('missing executables, signal termination and missing status report failure', t => {
  for (const result of [{ error: new Error('ENOENT'), status: null }, { signal: 'SIGTERM', status: null }, { status: null }]) {
    const f = fixture(t, { run: () => result });
    assert.equal(main(['test'], f.options), 1);
  }
});

test('help performs no subprocess or filesystem work', t => {
  const f = fixture(t);
  for (const args of [[], ['help'], ['--help'], ['-h']]) assert.equal(main(args, f.options), 0);
  assert.equal(f.calls.length, 0);
  assert.equal(existsSync(join(f.root, 'bin')), false);
});

test('help documents the same check order that check actually runs', t => {
  const f = fixture(t);
  const lines = [];
  f.options.log = line => lines.push(String(line));
  assert.equal(main(['help'], f.options), 0);
  assert.match(lines.join('\n'), /check 顺序执行 check-rules、fmt、lint、test/);
});
