// 维护工具共用的单会话 MySQL CLI。默认仅连接本项目开发库；显式指定容器或原生客户端可用于其它环境。
// 密码从环境传入，不输出命令或 SQL；--unbuffered 保证交互查询可在同一连接持有维护锁。
import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../..', import.meta.url));
export function openMySQL() {
  const flags = ['--batch', '--raw', '--skip-column-names', '--unbuffered', '--default-character-set=utf8mb4'];
  let command = 'docker';
  let args;
  const env = { ...process.env };
  if (env.MYSQL_CLIENT_CONTAINER) {
    env.MYSQL_PWD ||= env.MYSQL_PASSWORD || '';
    args = ['exec', '-i', '-e', 'MYSQL_PWD', env.MYSQL_CLIENT_CONTAINER, 'mysql', ...flags,
      '-h', env.MYSQL_HOST || '127.0.0.1', '-P', env.MYSQL_PORT || '3306', '-u', env.MYSQL_USER || 'root'];
  } else if (env.MYSQL_HOST || env.MYSQL_USER) {
    command = 'mysql';
    env.MYSQL_PWD ||= env.MYSQL_PASSWORD || '';
    args = [...flags, '-h', env.MYSQL_HOST || '127.0.0.1', '-P', env.MYSQL_PORT || '3306', '-u', env.MYSQL_USER || 'root'];
  } else {
    args = ['compose', '-f', 'docker/compose.dev.yaml', 'exec', '-T', 'mysql', 'sh', '-c',
      'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql --batch --raw --skip-column-names --unbuffered --default-character-set=utf8mb4 -uroot'];
  }
  const child = spawn(command, args, { cwd: root, env, stdio: ['pipe', 'pipe', 'pipe'] });
  let pending;
  let closed = false;
  let stderr = '';
  child.stderr.on('data', data => { stderr = (stderr + data).slice(-4096); });
  const fail = error => { closed = true; if (pending) { clearTimeout(pending.timer); pending.reject(error); pending = undefined; } };
  child.on('error', fail);
  child.stdin.on('error', fail);
  child.on('exit', code => fail(new Error(`MySQL client exited (${code}): ${stderr}`)));
  createInterface({ input: child.stdout }).on('line', line => {
    if (!pending) return;
    if (line === pending.marker) {
      clearTimeout(pending.timer);
      pending.resolve(pending.rows);
      pending = undefined;
    } else pending.rows.push(line);
  });
  return {
    async query(sql) {
      if (closed || pending) throw new Error('MySQL client closed or query already running');
      const marker = `done_${randomUUID()}`;
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => { child.kill(); fail(new Error('MySQL maintenance query timeout (120s)')); }, 120000);
        pending = { marker, rows: [], resolve, reject, timer };
        child.stdin.write(`${sql}\nSELECT '${marker}';\n`);
      });
    },
    close() { child.stdin.end(); },
  };
}
