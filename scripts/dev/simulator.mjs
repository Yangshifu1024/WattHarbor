// 前台启动 DC589 模拟器实例。模拟器是交互进程，不在后台重复拉起；
// 编译产物统一落 bin/（已在 .gitignore），.tmp 只留运行期状态文件。
// 用法：node scripts/dev/simulator.mjs [--number 1|2] [--ports 1-20] [--device-id 16位十进制]
import { spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { join } from 'node:path';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');
const IS_WINDOWS = process.platform === 'win32';

const { values } = parseArgs({
  options: {
    number: { type: 'string', default: '1' },
    ports: { type: 'string', default: '2' },
    'device-id': { type: 'string', default: '' },
  },
});
const number = Number(values.number);
const ports = Number(values.ports);
if (number !== 1 && number !== 2) {
  console.error('DC589 simulator number must be 1 or 2.');
  process.exit(2);
}
if (!Number.isInteger(ports) || ports < 1 || ports > 20) {
  console.error('DC589 simulator ports must be an integer between 1 and 20.');
  process.exit(2);
}

let deviceId = values['device-id'];
if (!deviceId) {
  // 与 prepare.mjs 导入的两台设备保持一致，1 号机端口接第一台。
  deviceId = number === 1 ? '5348240514082652' : '5348240514082653';
}
if (!/^\d{16}$/.test(deviceId)) {
  console.error('DC589 device ID must be exactly 16 decimal digits.');
  process.exit(2);
}

mkdirSync(join(ROOT, '.tmp'), { recursive: true });
mkdirSync(join(ROOT, 'bin'), { recursive: true });
const binary = join(ROOT, 'bin', `simulator-dc589-${number}${IS_WINDOWS ? '.exe' : ''}`);

const build = spawnSync('go', ['build', '-o', binary, './cmd/simulator-dc589'], { cwd: ROOT, stdio: 'inherit' });
if (build.status !== 0) {
  console.error('Simulator build failed. Go 1.27 is required.');
  process.exit(1);
}

// 控制口按实例错开：1 号 9190、2 号 9191；状态文件按实例与设备区分，
// 保存进行中的充电与待确认报告，支持进程重启恢复。
const control = `127.0.0.1:${9189 + number}`;
const state = join(ROOT, '.tmp', `simulator-${number}-${deviceId}.json`);
const run = spawnSync(binary, ['-board-id', deviceId, '-ports', String(ports), '-control', control, '-state-file', state], { stdio: 'inherit' });
if (run.status !== 0) {
  console.error('Simulator stopped with an error. Check the displayed message.');
  process.exit(run.status ?? 1);
}
