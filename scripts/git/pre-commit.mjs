// pre-commit 钩子逻辑：跑 go vet 与默认测试，任何一步失败即拒绝提交。
// 直接调用 Go 命令而不经过 make，钩子在无 make 的系统上同样可用；
// 格式化请显式执行 make fmt 后再暂存，钩子不代做格式化。
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');

for (const [label, cmd, args] of [
  ['go vet', 'go', ['vet', './...']],
  ['go test', 'go', ['test', './...']],
]) {
  const result = spawnSync(cmd, args, { cwd: ROOT, stdio: 'inherit' });
  if (result.status !== 0) {
    console.error(`pre-commit aborted: ${label} failed`);
    process.exit(result.status ?? 1);
  }
}
