// pre-commit 钩子逻辑：先跑规则层检查，再跑 go vet 与默认测试，任何一步失败即拒绝提交。
// 直接调用 Node 与 Go 命令，钩子仅需要 Node.js 与 Go；
// 规则层检查在仓库根执行，go 命令在后端模块目录 backend/ 下执行。
// 格式化请显式执行 node tasks.mjs fmt 后再暂存，钩子不代做格式化。
// 用法：node scripts/git/pre-commit.mjs（不接受参数）
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { join, resolve } from 'node:path';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');
const BACKEND = join(ROOT, 'backend');

// 步骤顺序即成本顺序：规则层检查秒级完成且只读文档，放最前面才能让文档漂移
// 在跑 go 之前就被拦下，而不是让维护者先等一遍 fmt 与 vet。
const STEPS = [
  { label: '规则层检查', command: process.execPath, args: [join(ROOT, 'scripts/rules/check-rules.mjs')], cwd: ROOT },
  { label: 'go vet', command: 'go', args: ['vet', './...'], cwd: BACKEND },
  { label: 'go test', command: 'go', args: ['test', './...'], cwd: BACKEND },
];

function spawn(step) {
  return spawnSync(step.command, step.args, { cwd: step.cwd, stdio: 'inherit' });
}

// main 逐步执行并在第一步失败处返回它的退出码；全部通过返回 0，多余参数返回 2。
// run 可替换以测试步骤顺序与中止行为，不必真跑 go。
export function main(args = [], { run = spawn, log = console.log, error = console.error } = {}) {
  if (args.length > 0) {
    error('用法：node scripts/git/pre-commit.mjs（不接受参数）');
    return 2;
  }
  for (const step of STEPS) {
    const result = run(step);
    if (result.status !== 0) {
      error(`pre-commit aborted: ${step.label} failed`);
      return result.status ?? 1;
    }
    log(`pre-commit ok: ${step.label}`);
  }
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) process.exitCode = main(process.argv.slice(2));
