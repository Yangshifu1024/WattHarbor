// 检查 docker/compose.prod.yaml 的部署配置，不启动服务。
// --config-only 仅跳过凭据值检查，仍验证拓扑、挂载路径及 Caddy 配置。
import { readFileSync, statSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { isIP } from 'node:net';

const root = fileURLToPath(new URL('../../', import.meta.url));
const applicationNames = ['user', 'admin', 'central', 'gateway', 'worker'];

// requireValue 在条件不成立时抛出错误；消息不得包含凭据。
function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

// docker 在仓库根执行命令并返回输出；失败只显示固定错误，避免泄露凭据。
function docker(arguments_, message) {
  const result = spawnSync('docker', arguments_, { cwd: root, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: 120000 });
  requireValue(!result.error && result.status === 0, message);
  return result.stdout;
}

// databaseURL 解析数据库地址并核对 MySQL 库名；错误不得包含连接字符串。
function databaseURL(environment, key, schema) {
  let url;
  try { url = new URL(environment[key]); } catch { throw new Error(`${key} is invalid.`); }
  requireValue(url.protocol === 'mysql:' && url.pathname === `/${schema}`, `${key} must target ${schema}.`);
  return url;
}

// redisArgument 读取 Redis 命令中指定参数的值；未配置返回 undefined，命令不是数组时报错。
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

// validateDeployment 检查展开后的 Compose 和 Caddy 配置；configOnly 仅跳过凭据值检查。
// 失败时抛出不含连接字符串、令牌和密码的错误。
export function validateDeployment(config, { configOnly = false, caddyText } = {}) {
  const services = config?.services;
  requireValue(services && typeof services === 'object', 'Compose services are missing.');
  for (const name of ['mysql', ...applicationNames, 'migrate', 'provision-runtime', 'caddy', 'redis-cache', 'redis-stream']) {
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
    requireValue(url.host === centralDB.host && url.pathname === centralDB.pathname, `${name} must use the shared central database URL.`);
  }
  for (const [key, schema, owner] of [['DATABASE_URL_GATEWAY', 'gateway_db', 'gateway'], ['DATABASE_URL_WORKER', 'worker_db', 'worker']]) {
    const ownerKey = owner === 'gateway' ? 'DATABASE_URL' : key;
    const ownerDB = databaseURL(services[`chargepilot-${owner}`].environment ?? {}, ownerKey, schema);
    for (const name of ['worker', 'migrate']) {
      const url = databaseURL(services[`chargepilot-${name}`].environment ?? {}, key, schema);
      requireValue(url.host === ownerDB.host && url.pathname === ownerDB.pathname, `${name} must use the ${schema} database URL.`);
    }
  }

  const provision = services['chargepilot-provision-runtime'];
  requireValue(provision.depends_on?.['chargepilot-migrate']?.condition === 'service_completed_successfully', 'Runtime accounts must be provisioned after migration.');
  for (const name of applicationNames) {
    const service = services[`chargepilot-${name}`];
    requireValue(service.depends_on?.['chargepilot-provision-runtime']?.condition === 'service_completed_successfully', `${name} must wait for runtime accounts.`);
    for (const key of name === 'worker' ? ['DATABASE_URL_CENTRAL','DATABASE_URL_GATEWAY','DATABASE_URL_WORKER'] : [name === 'gateway' ? 'DATABASE_URL' : 'DATABASE_URL_CENTRAL']) {
      const url = new URL(service.environment[key]);
      requireValue(url.username === `chargepilot_${name}`, `${name} must use its own runtime database account.`);
      if (!configOnly) {
        const password = provision.environment?.[`DB_${name.toUpperCase()}_PASSWORD`];
        requireValue(typeof password === 'string' && password.length > 0 && !password.startsWith('change-me'), `${name} database password is missing or still uses a template value.`);
        requireValue(decodeURIComponent(url.password) === password, `${name} database password must match account provisioning.`);
      }
    }
  }
  for (const key of ['DATABASE_URL_CENTRAL','DATABASE_URL_GATEWAY','DATABASE_URL_WORKER']) {
    requireValue(new URL(services['chargepilot-migrate'].environment[key]).username === 'chargepilot', 'Migration must use the maintenance database account.');
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

  // 后台仅信任专用代理网络内 Caddy 的单一地址，默认业务网络里的其他服务不能伪造来源。
  const proxyNetwork = config.networks?.['admin-proxy'];
  const proxyIP = services['chargepilot-caddy'].networks?.['admin-proxy']?.ipv4_address;
  requireValue(proxyNetwork?.internal === true && isIP(proxyIP ?? '') === 4, 'Admin proxy requires an internal network and fixed Caddy IPv4 address.');
  requireValue(services['chargepilot-admin'].environment?.ADMIN_TRUSTED_PROXIES === proxyIP, 'Admin must trust only the fixed Caddy proxy address.');
  requireValue(services['chargepilot-admin'].networks?.['admin-proxy']?.aliases?.includes('admin-proxy-upstream'), 'Admin proxy upstream alias is missing.');
  for (const [name, service] of Object.entries(services)) {
    requireValue(!service.networks?.['admin-proxy'] || ['chargepilot-admin', 'chargepilot-caddy'].includes(name), 'Only admin and Caddy may join the admin proxy network.');
  }
  requireValue(typeof caddyText === 'string', 'Caddy configuration text is missing.');
  const internal = caddyBlock(caddyText, '/api/v1/internal/*');
  requireValue(/^\s*respond 404\s*$/m.test(internal) && !/\breverse_proxy\b/.test(internal), 'Caddy must block the internal API routes.');
  for (const [path, destination] of [['/api/v1/admin/*', 'admin-proxy-upstream:8081'], ['/api/v1/user/*', 'chargepilot-user:8080'], ['/api/v1/public/*', 'chargepilot-user:8080']]) {
    requireValue(new RegExp(`^\\s*reverse_proxy ${destination}\\s*$`, 'm').test(caddyBlock(caddyText, path)), `Caddy must route ${path} to ${destination}.`);
  }
  requireValue(/^\s*respond 404\s*$/m.test(caddyBlock(caddyText, '/api/*')), 'Caddy must reject other API routes.');
}

// validatePaths 检查挂载源和 Dockerfile 是否存在，读取失败时终止。
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

// main 检查 Compose 配置并执行 Caddy 语法验证；成功返回 0，配置错误返回 1，用法错误返回 2。
// 不输出含凭据的展开配置；导入模块时不执行命令。
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
