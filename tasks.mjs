// 仓库任务入口：Node.js 24，直接调用 Go／Docker，不依赖 shell 或 Make。
// 用法：node tasks.mjs <任务>；路径以本文件定位，调用方可位于任意目录。
import { spawnSync } from 'node:child_process';
import { mkdirSync, rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = dirname(fileURLToPath(import.meta.url));
const SERVICES = ['user', 'admin', 'central', 'gateway', 'worker', 'migrate', 'devseed', 'simulator-dc589'];
const TASKS = ['fmt', 'lint', 'test', 'test-integration', 'build', 'build-all', 'check', 'clean', 'dev-up', 'dev-down'];
const HELP = `用法：node tasks.mjs <任务>
任务：${TASKS.join('、')}
单服务构建：node tasks.mjs build <服务>（也可设置 SERVICE 环境变量）
服务：${SERVICES.join('、')}
真库测试：先设置 TEST_MYSQL_URL 与所需的 TEST_REDIS_URL，再执行 test-integration。
自动准备开发数据库并测试：node scripts/db/test-integration.mjs
check 顺序执行 fmt、lint、test；构建产物位于 bin/。
clean 仅删除 bin/；dev-down 保留容器与数据卷。`;

// main 校验 argv 并顺序执行一个任务，返回退出码；不修改调用方环境。
// options 的 env／platform 用于环境与平台选择，run／root／log／error 用于隔离验证。
// 子命令失败立即返回其退出码；启动失败、信号终止或文件操作失败返回 1，参数错误返回 2。
// 构建创建 root/bin，clean 仅删除该目录；开发任务会启动或停止现有 Compose 栈。
export function main(argv, {
  env = process.env, platform = process.platform, run = spawnSync,
  root = ROOT, log = console.log, error = console.error,
} = {}) {
  const [task, service, ...extra] = argv;
  if (argv.length === 0 || (argv.length === 1 && ['help', '--help', '-h'].includes(task))) {
    log(HELP);
    return 0;
  }
  if (!TASKS.includes(task) || extra.length || (task !== 'build' && service !== undefined)) {
    error(HELP);
    return 2;
  }
  const selected = service ?? env.SERVICE;
  if (task === 'build' && !SERVICES.includes(selected)) {
    error(`用法：node tasks.mjs build <服务>；服务：${SERVICES.join('、')}`);
    return 2;
  }
  if (task === 'test-integration' && !env.TEST_MYSQL_URL?.trim()) {
    error('请先设置 TEST_MYSQL_URL；或执行 node scripts/db/test-integration.mjs 自动准备开发环境。');
    return 2;
  }
  const backend = join(root, 'backend');
  const bin = join(root, 'bin');
  const executable = platform === 'win32' ? 'go.exe' : 'go';
  const commands = {
    fmt: [[executable, ['fmt', './...'], backend]],
    lint: [[executable, ['vet', './...'], backend]],
    test: [[executable, ['test', './...'], backend]],
    'test-integration': [[executable, ['test', '-tags=integration', '-count=1', './...'], backend]],
    build: [[executable, ['build', '-trimpath', '-o', join(bin, `${selected}${platform === 'win32' ? '.exe' : ''}`), `./cmd/${selected}`], backend]],
    'build-all': [[executable, ['build', '-trimpath', '-o', `${bin}/`, './cmd/...'], backend]],
    'dev-up': [[process.execPath, [join(root, 'scripts/dev/start.mjs')], root]],
    'dev-down': [[platform === 'win32' ? 'docker.exe' : 'docker', ['compose', '-f', join(root, 'docker/compose.dev.yaml'), 'stop'], root]],
  };
  try {
    if (task === 'clean') {
      rmSync(bin, { recursive: true, force: true });
      return 0;
    }
    if (['build', 'build-all'].includes(task)) mkdirSync(bin, { recursive: true });
    const sequence = task === 'check' ? [...commands.fmt, ...commands.lint, ...commands.test] : commands[task];
    for (const [command, args, cwd] of sequence) {
      const result = run(command, args, { cwd, env, stdio: 'inherit', shell: false });
      if (result.error || result.signal) {
        error(`任务 ${task} 未完成：${result.error?.message ?? `子进程被 ${result.signal} 终止`}`);
        return 1;
      }
      if (result.status !== 0) return result.status ?? 1;
    }
    return 0;
  } catch (failure) {
    error(`任务 ${task} 未完成：${failure.message}`);
    return 1;
  }
}

// 仅直接运行时执行任务；导入模块供验证时不产生文件或进程副作用。
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
