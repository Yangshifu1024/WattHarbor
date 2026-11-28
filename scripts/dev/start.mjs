// 启动本地开发栈（deploy/compose.dev.yaml）。保留既有数据；结构重建见 reset-dev.mjs。
// 用法：node scripts/dev/start.mjs
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');

const up = spawnSync('docker', ['compose', '-f', `${ROOT}/deploy/compose.dev.yaml`, 'up', '-d', '--wait', '--remove-orphans'], { stdio: 'inherit' });
if (up.status !== 0) {
  console.error('Development stack did not start. Check Docker Desktop and compose logs.');
  process.exit(1);
}

console.log('Admin: http://127.0.0.1:5173 (admin / ChangeMe!Admin2026)');
console.log('WattHarbor API: http://127.0.0.1:8080');
console.log('User H5: http://127.0.0.1:5174 (development account tester-1)');
console.log('First install can take a minute. Open two terminals for node scripts/dev/simulator.mjs --number 1 and --number 2.');
console.log('See docs/operations.md for development and deployment. Existing development data is preserved.');
