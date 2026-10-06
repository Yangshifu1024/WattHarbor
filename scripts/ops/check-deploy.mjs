// 检查根生产 Compose 的部署配置；不启动应用、数据库或设备服务。
// --config-only 仅跳过凭据值检查，仍验证拓扑、挂载路径及 Caddy 配置。
import { readFileSync, statSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';

const root = fileURLToPath(new URL('../../', import.meta.url));
const applicationNames = ['user', 'admin', 'central', 'gateway', 'worker'];

// requireValue 在 condition 不成立时抛出固定 message；调用方不得将解析后的秘密值拼入消息。
function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

// docker 在仓库根目录执行 arguments_，成功返回 stdout；失败只报告固定 message，不输出含凭据的进程输出。
function docker(arguments_, message) {
  const result = spawnSync('docker', arguments_, { cwd: root, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: 120000 });
  requireValue(!result.error && result.status === 0, message);
  return result.stdout;
}

// databaseURL 解析 environment[key] 并要求 MySQL 的 schema；返回 URL，解析及 schema 不匹配时抛出不含连接值的错误。
function databaseURL(environment, key, schema) {
  let url;
  try { url = new URL(environment[key]); } catch { throw new Error(`${key} is invalid.`); }
  requireValue(url.protocol === 'mysql:' && url.pathname === `/${schema}`, `${key} must target ${schema}.`);
  return url;
}

// redisArgument 从 Redis service 的原 command 参数数组取 key 的后续值；未配置返回 undefined，非数组命令报错。
function redisArgument(service, key) {
  requireValue(Array.isArray(service.command), 'Redis command must be an argument list.');
  const index = service.command.indexOf(key);
  return index < 0 ? undefined : service.command[index + 1];
}

// caddyBlock 读取 text 中指定 path 的 handle 块正文，返回正文或空串；忽略注释，避免把注释中的配置当作生效路由。
// 此处核对部署路由声明，完整 Caddy 语法仍由 CLI 的 caddy adapt --validate 检查。
function caddyBlock(text, path) {
  const source = text.replace(/^\s*#.*$/gm, '');
  const escaped = path.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = source.match(new RegExp(`^\\s*handle ${escaped} \\{([\\s\\S]*?)^\\s*\\}`, 'm'));
  return match?.[1] ?? '';
}

// validateDeployment 检查已展开的 config 和 caddyText；configOnly 跳过凭据值校验，其余部署约束保持启用。
// 返回 undefined 或抛出不含连接字符串、令牌和密码值的错误；无文件、网络、进程或数据库副作用。
export function validateDeployment(config, { configOnly = false, caddyText } = {}) {
  const services = config?.services;
  requireValue(services && typeof services === 'object', 'Compose services are missing.');
  for (const name of ['mysql', ...applicationNames, 'migrate', 'caddy', 'redis-cache', 'redis-stream']) {
    requireValue(services[`chargepilot-${name}`], `Missing service: chargepilot-${name}.`);
  }
  for (const [name, service] of Object.entries(services)) {
    for (const port of service.ports ?? []) {
      const allowed = name === 'chargepilot-caddy' ? [80, 443] : name === 'chargepilot-gateway' ? [9100] : [];
      requireValue(allowed.includes(Number(port.target)) && Number(port.published) === Number(port.target), `Unexpected published port on ${name}.`);
    }
  }

  const central = services['chargepilot-central'].environment ?? {};
  for (const key of ['DATABASE_URL_USER', 'DATABASE_URL_ADMIN', 'DATABASE_URL_BILLING', 'DATABASE_URL_GATEWAY', 'DATABASE_URL_WORKER']) {
    requireValue(!Object.hasOwn(central, key), 'Central must use one database URL.');
  }
  const centralDB = databaseURL(central, 'DATABASE_URL_CENTRAL', 'central_db');
  for (const name of ['user', 'admin', 'worker', 'migrate']) {
    const url = databaseURL(services[`chargepilot-${name}`].environment ?? {}, 'DATABASE_URL_CENTRAL', 'central_db');
    requireValue(url.href === centralDB.href, `${name} must use the shared central database URL.`);
  }
  for (const [key, schema, owner] of [['DATABASE_URL_GATEWAY', 'gateway_db', 'gateway'], ['DATABASE_URL_WORKER', 'worker_db', 'worker']]) {
    const ownerKey = owner === 'gateway' ? 'DATABASE_URL' : key;
    const ownerDB = databaseURL(services[`chargepilot-${owner}`].environment ?? {}, ownerKey, schema);
    for (const name of ['worker', 'migrate']) {
      const url = databaseURL(services[`chargepilot-${name}`].environment ?? {}, key, schema);
      requireValue(url.href === ownerDB.href, `${name} must use the ${schema} database URL.`);
    }
  }

  // 只检查部署配置的实际凭据归属，不扫描源码或建立架构检查规则。
  for (const name of applicationNames) {
    const environment = services[`chargepilot-${name}`].environment ?? {};
    const disallowed = [];
    if (name !== 'user' && name !== 'admin') disallowed.push('JWT_SECRET');
    if (name !== 'user') disallowed.push('WECHAT_SECRET', 'LOGIN_MODE');
    if (name !== 'worker') disallowed.push('REDIS_STREAM_URL');
    if (!['user', 'admin', 'central'].includes(name)) disallowed.push('REDIS_CACHE_URL');
    if (name !== 'admin') disallowed.push('ADMIN_BOOTSTRAP_USER', 'ADMIN_BOOTSTRAP_PASSWORD');
    if (name !== 'user' && name !== 'central') disallowed.push('WECHAT_APPID', 'PAYMENT_MODE', 'WECHAT_MCH_ID', 'WECHAT_CERT_SERIAL', 'WECHAT_APIV3_KEY', 'WECHAT_PRIVATE_KEY_PATH', 'WECHAT_NOTIFY_URL');
    for (const key of disallowed) requireValue(!Object.hasOwn(environment, key), `${name} must not receive ${key}.`);
  }
  for (const [name, policy] of [['redis-cache', 'allkeys-lru'], ['redis-stream', 'noeviction']]) {
    requireValue(redisArgument(services[`chargepilot-${name}`], '--maxmemory-policy') === policy, `${name} must use ${policy}.`);
  }

  const user = services['chargepilot-user'].environment ?? {};
  const userMode = user.PAYMENT_MODE || 'disabled';
  const centralMode = central.PAYMENT_MODE || 'disabled';
  for (const [name, mode] of [['user', userMode], ['central', centralMode]]) {
    requireValue(['disabled', 'simulation', 'wechat_direct'].includes(mode), `${name} has invalid PAYMENT_MODE.`);
  }
  requireValue(userMode === centralMode, 'user and central must use the same PAYMENT_MODE.');
  const loginMode = user.LOGIN_MODE || 'wechat';
  requireValue(['wechat', 'development'].includes(loginMode), 'user has invalid LOGIN_MODE.');
  requireValue(loginMode !== 'development' || userMode === 'simulation', 'Development login requires PAYMENT_MODE=simulation.');

  if (!configOnly) {
    const secrets = {
      MYSQL_ROOT_PASSWORD: services['chargepilot-mysql'].environment?.MYSQL_ROOT_PASSWORD,
      DB_PASSWORD: services['chargepilot-mysql'].environment?.MYSQL_PASSWORD,
      'user.JWT_SECRET': user.JWT_SECRET,
      'admin.JWT_SECRET': services['chargepilot-admin'].environment?.JWT_SECRET,
      REDIS_PASSWORD: redisArgument(services['chargepilot-redis-cache'], '--requirepass'),
      REDIS_STREAM_PASSWORD: redisArgument(services['chargepilot-redis-stream'], '--requirepass'),
      'user.WECHAT_SECRET': user.WECHAT_SECRET,
    };
    for (const name of applicationNames) secrets[`${name}.SERVICE_TOKEN`] = services[`chargepilot-${name}`].environment?.SERVICE_TOKEN;
    for (const [name, value] of Object.entries(secrets)) {
      requireValue(typeof value === 'string' && value.length > 0 && !value.startsWith('change-me') && value !== 'your_app_secret', `${name} is missing or still uses a template value.`);
    }
    for (const name of ['user', 'admin']) requireValue(services[`chargepilot-${name}`].environment.JWT_SECRET.length >= 32, `${name}.JWT_SECRET must contain at least 32 characters.`);
    for (const name of applicationNames) requireValue(services[`chargepilot-${name}`].environment.SERVICE_TOKEN === central.SERVICE_TOKEN, `${name} must use the shared SERVICE_TOKEN.`);
    for (const name of ['user', 'central']) {
      const environment = services[`chargepilot-${name}`].environment;
      requireValue(typeof environment.WECHAT_APPID === 'string' && environment.WECHAT_APPID.length > 0 && environment.WECHAT_APPID !== 'wx_your_appid', `${name}.WECHAT_APPID is missing or still uses a template value.`);
      if (userMode === 'wechat_direct') {
        for (const key of ['WECHAT_MCH_ID', 'WECHAT_CERT_SERIAL', 'WECHAT_PRIVATE_KEY_PATH', 'WECHAT_NOTIFY_URL']) {
          requireValue(typeof environment[key] === 'string' && environment[key].length > 0 && environment[key] !== 'your_merchant_id', `${name}.${key} is missing or still uses a template value.`);
        }
        requireValue(typeof environment.WECHAT_APIV3_KEY === 'string' && Buffer.byteLength(environment.WECHAT_APIV3_KEY, 'utf8') === 32, `${name}.WECHAT_APIV3_KEY must contain 32 bytes.`);
      }
    }
    requireValue(user.WECHAT_APPID === central.WECHAT_APPID, 'user and central must use the same WECHAT_APPID.');
  }

  requireValue(typeof caddyText === 'string', 'Caddy configuration text is missing.');
  const internal = caddyBlock(caddyText, '/api/v1/internal/*');
  requireValue(/^\s*respond 404\s*$/m.test(internal) && !/\breverse_proxy\b/.test(internal), 'Caddy must block the internal API routes.');
  for (const [path, destination] of [['/api/v1/admin/*', 'chargepilot-admin:8081'], ['/api/v1/user/*', 'chargepilot-user:8080'], ['/api/v1/public/*', 'chargepilot-user:8080']]) {
    requireValue(new RegExp(`^\\s*reverse_proxy ${destination}\\s*$`, 'm').test(caddyBlock(caddyText, path)), `Caddy must route ${path} to ${destination}.`);
  }
  requireValue(/^\s*respond 404\s*$/m.test(caddyBlock(caddyText, '/api/*')), 'Caddy must reject other API routes.');
}

// validatePaths 对 config 中的 bind 源及 Dockerfile 保持原存在性检查；stat 失败直接终止，不启动容器。
function validatePaths(config) {
  for (const [name, service] of Object.entries(config.services)) {
    for (const volume of service.volumes ?? []) {
      if (volume.type !== 'bind') continue;
      const info = statSync(volume.source);
      requireValue(info.isFile() || info.isDirectory(), `Invalid bind path on ${name}.`);
    }
    if (service.build) statSync(resolve(service.build.context, service.build.dockerfile));
  }
}

// main 使用 args 执行 CLI；返回 0、配置失败的 1 或用法错误的 2，保留 Compose 及无网络 Caddy adapt 验证。
// 不输出展开的 JSON；只有直接运行此文件才调用，导入纯校验函数不会执行 Docker 或读取真实配置。
export function main(args = process.argv.slice(2)) {
  if (args.some(arg => arg !== '--config-only')) {
    console.error('Usage: node scripts/ops/check-deploy.mjs [--config-only]');
    return 2;
  }
  try {
    const output = docker(['compose', '-f', 'docker/compose.prod.yaml', 'config', '--format', 'json'], 'Compose validation failed. Check .env and run docker compose config --quiet.');
    let config;
    try { config = JSON.parse(output); } catch { throw new Error('Unable to read Compose configuration JSON.'); }
    const caddy = resolve(root, 'docker/Caddyfile');
    validateDeployment(config, { configOnly: args.includes('--config-only'), caddyText: readFileSync(caddy, 'utf8') });
    validatePaths(config);
    console.log('Compose and structure checks passed; validating Caddy configuration...');
    docker(['run', '--rm', '--network', 'none', '-e', `PUBLIC_DOMAIN=${config.services['chargepilot-caddy'].environment.PUBLIC_DOMAIN}`, '-v', `${caddy}:/etc/caddy/Caddyfile:ro`, config.services['chargepilot-caddy'].image, 'caddy', 'adapt', '--config', '/etc/caddy/Caddyfile', '--validate'], 'Caddy validation failed. Check Docker image availability and docker/Caddyfile.');
    console.log('Compose, bind paths, ports, database URLs, Redis policies and Caddy validation passed.');
    return 0;
  } catch (error) {
    console.error(error.message);
    return 1;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) process.exitCode = main();
