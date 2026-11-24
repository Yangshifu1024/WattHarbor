// 迁移后配置单体的运行账号及其直接权限；不改变维护账号。
// Node.js 24 + MySQL CLI，可从任意工作目录运行；不依赖 shell、sed 或临时 SQL 文件。
// 凭据从环境传入，SQL 仅经 stdin；失败不转发可能包含密码的客户端诊断。
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const layouts = new Map([
  ['--unified', ['wattharbor']],
  ['--three-schema', ['central_db', 'gateway_db', 'worker_db']],
]);
const account = "'wattharbor_app'@'%'";

// buildProvisionSQL 只允许固定库名，密码在 NO_BACKSLASH_ESCAPES 下加倍单引号。
export function buildProvisionSQL(env, layout) {
  const schemas = layouts.get(layout);
  if (!schemas) throw new Error('Expected --unified or --three-schema');
  for (const key of ['MYSQL_ROOT_PASSWORD', 'DB_APP_PASSWORD']) {
    if (typeof env[key] !== 'string' || !env[key].length || env[key].includes('\0')) {
      throw new Error(`${key} is required and must not contain NUL`);
    }
  }
  const password = env.DB_APP_PASSWORD.replaceAll("'", "''");
  const statements = [
    "SET SESSION sql_mode='NO_BACKSLASH_ESCAPES';",
    `CREATE USER IF NOT EXISTS ${account} IDENTIFIED BY '${password}';`,
    `ALTER USER ${account} IDENTIFIED BY '${password}';`,
    `REVOKE ALL PRIVILEGES, GRANT OPTION FROM ${account};`,
  ];
  for (const schema of schemas) {
    // 库级授权会解释下划线通配符，转义后只授权指定 schema。
    const exactSchema = schema.replaceAll('_', '\\_');
    statements.push(`GRANT SELECT, INSERT, UPDATE, DELETE ON \`${exactSchema}\`.* TO ${account};`);
  }
  return statements.join('\n') + '\n';
}

// main 使用同一客户端会话提交固定权限 SQL；返回失败码且不输出 SQL、密码或客户端原始错误。
export function main(args = process.argv.slice(2), env = process.env) {
  if (args.length !== 1 || !layouts.has(args[0])) {
    console.error('Usage: node scripts/db/provision-runtime.mjs --unified|--three-schema');
    return 2;
  }
  try {
    const sql = buildProvisionSQL(env, args[0]);
    const connection = env.MYSQL_SOCKET
      ? ['--protocol=SOCKET', '--socket', env.MYSQL_SOCKET]
      : ['--protocol=TCP', '--host', env.MYSQL_HOST || '127.0.0.1', '--port', env.MYSQL_PORT || '3306'];
    const result = spawnSync('mysql', [...connection, '--user', 'root', '--batch', '--binary-mode', '--default-character-set=utf8mb4'], {
      env: { ...env, MYSQL_PWD: env.MYSQL_ROOT_PASSWORD }, input: sql, encoding: 'utf8', timeout: 60000,
    });
    if (result.error?.code === 'ENOENT') throw new Error('MySQL client is required');
    if (result.error || result.status !== 0) throw new Error('Runtime account provisioning failed; check MySQL connectivity and root permissions');
    console.log('Runtime database account provisioned.');
    return 0;
  } catch (error) { console.error(error.message); return 1; }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) process.exitCode = main();
