// 安装 Git 钩子：把 core.hooksPath 指向 .githooks，并确保钩子文件可执行。
// 钩子用可执行的 POSIX 脚本调用 scripts/git/pre-commit.mjs。
// 用法：node scripts/git/install-hooks.mjs
import { spawnSync } from 'node:child_process';
import { chmodSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');

const configured = spawnSync('git', ['-C', ROOT, 'config', 'core.hooksPath', '.githooks'], { stdio: 'inherit' });
if (configured.status !== 0) {
  console.error('Unable to install Git hooks');
  process.exit(1);
}
chmodSync(join(ROOT, '.githooks', 'pre-commit'), 0o755);
console.log('pre-commit enabled: check-rules + go vet + go test (requires Go 1.27)');
