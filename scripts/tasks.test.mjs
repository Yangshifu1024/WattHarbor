// 验证任务编排、错误停止与跨平台路径；不启动开发服务，不连接数据库。
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { main } from '../tasks.mjs';

// fixture 建立独立仓库目录并登记清理，记录子进程调用；返回 root、calls 与运行选项。
// 子命令默认返回成功，调用方可覆盖 run／env／platform 以复现故障；无真实进程副作用。
function fixture(t, overrides = {}) {
  const root = mkdtempSync(join(tmpdir(), 'chargepilot-tasks-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const calls = [];
  const options = {
    root, env: {}, platform: 'linux', log() {}, error() {},
    run(command, args, options) { calls.push({ command, args, options }); return { status: 0 }; },
    ...overrides,
  };
  return { root, calls, options };
}

test('check runs formatting, vet and tests in order from backend', t => {
  const f = fixture(t);
  assert.equal(main(['check'], f.options), 0);
  assert.deepEqual(f.calls.map(c => c.args), [['fmt', './...'], ['vet', './...'], ['test', './...']]);
  for (const c of f.calls) {
    assert.equal(c.options.cwd, join(f.root, 'backend'));
    assert.equal(c.options.shell, false);
  }
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
