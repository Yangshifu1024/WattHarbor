// 启动本地开发栈（docker/compose.dev.yaml）。保留既有数据；结构重建见 reset-dev.mjs。
// 用法：node scripts/dev/start.mjs
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../..', import.meta.url)).replace(/[\\/]$/, '');

const up = spawnSync('docker', ['compose', '-f', `${ROOT}/docker/compose.dev.yaml`, 'up', '-d'], { stdio: 'inherit' });
if (up.status !== 0) {
  console.error('Development stack did not start. Check Docker Desktop and compose logs.');
  process.exit(1);
}

console.log('Admin: http://127.0.0.1:5173 (admin / ChangeMe!Admin2026)');
console.log('Admin API: http://127.0.0.1:8081');
console.log('User H5: http://127.0.0.1:5174 (development account tester-1)');
console.log('First install can take a minute. Open two terminals for node scripts/dev/simulator.mjs --number 1 and --number 2.');
console.log('Architecture and roadmap: docs/architecture-boundaries.md and docs/refactor-roadmap.md. This command preserves existing development data.');
