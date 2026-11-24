// 检查生产单体编排，不启动应用。--config-only 仅跳过凭据值检查。
import { existsSync, readFileSync, statSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { isIP } from 'node:net';

const root = fileURLToPath(new URL('../../', import.meta.url));
const databaseKeys = ['DATABASE_URL', 'DATABASE_URL_CENTRAL', 'DATABASE_URL_GATEWAY', 'DATABASE_URL_WORKER'];
const publicPaths = ['/', '/admin', '/admin/*', '/h5', '/h5/*', '/api/v1/admin/*', '/api/v1/user/*', '/api/v1/public/*'];

function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

// 不转发 Docker 或 URL 解析器的原始错误，避免输出凭据。
function docker(args, message) {
  const result = spawnSync('docker', args, { cwd: root, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024, timeout: 120000 });
  requireValue(!result.error && result.status === 0, message);
  return result.stdout;
}

function parseURL(raw, key, protocol) {
  let url;
  try {
    url = new URL(raw);
    decodeURIComponent(url.password);
  } catch {
    throw new Error(`${key} is invalid.`);
  }
  requireValue(url.protocol === protocol, `${key} must use ${protocol}`);
  return url;
}

function validatePorts(services) {
  for (const [name, service] of Object.entries(services)) {
    const expected = name === 'caddy' ? [80, 443] : name === 'app' ? [9100] : [];
    const ports = service.ports ?? [];
    requireValue(ports.length === expected.length && expected.every(expectedPort =>
      ports.some(port => Number(port.target) === expectedPort && Number(port.published) === expectedPort &&
        (port.protocol ?? 'tcp') === 'tcp')), `Unexpected published ports on ${name}.`);
  }
}

function validateStartup({ app, cache, caddy, migrate, mysql, 'provision-runtime': provision, stream }) {
  const env = app.environment ?? {};
  requireValue(String(env.APP_UNIFIED_DATABASE) === 'true', 'app must use the unified database.');
  requireValue(JSON.stringify(migrate.command) === JSON.stringify(['/usr/local/bin/migrate', '-schema', 'unified']),
    'migrate must initialize the unified database.');
  requireValue(app.image && app.image === migrate.image, 'app and migrate must use the same application image.');
  requireValue(migrate.depends_on?.mysql?.condition === 'service_healthy', 'migrate must wait for MySQL.');
  requireValue(provision.depends_on?.migrate?.condition === 'service_completed_successfully' &&
    app.depends_on?.['provision-runtime']?.condition === 'service_completed_successfully',
  'Provisioning must finish after migrations and before app starts.');
  requireValue(provision.network_mode === 'none' && provision.command?.length === 1 && provision.command[0] === '--unified',
    'provision-runtime must use unified mode without network access.');
  const mysqlSocket = mysql.volumes?.find(volume => volume.target === '/var/run/mysqld');
  const provisionSocket = provision.volumes?.find(volume => volume.target === '/var/run/mysqld');
  requireValue(mysqlSocket?.type === 'volume' && provisionSocket?.type === 'volume' &&
    mysqlSocket.source === provisionSocket.source && provisionSocket.read_only === true &&
    provision.environment?.MYSQL_SOCKET === '/var/run/mysqld/mysqld.sock',
  'provision-runtime must use the shared MySQL socket mounted read-only.');
  requireValue(app.depends_on?.cache?.condition === 'service_healthy' &&
    app.depends_on?.stream?.condition === 'service_healthy' && cache.healthcheck?.test && stream.healthcheck?.test,
  'app must wait for both Redis services.');
  requireValue(app.healthcheck?.test?.includes('http://127.0.0.1:8080/health/ready') &&
    caddy.depends_on?.app?.condition === 'service_healthy', 'Caddy must wait for app readiness.');
}

function validateDatabase(services, configOnly) {
  const { app, mysql, migrate, 'provision-runtime': provision } = services;
  for (const [name, service] of Object.entries(services)) {
    if (name === 'mysql' || name === 'provision-runtime') continue;
    requireValue(!Object.hasOwn(service.environment ?? {}, 'MYSQL_ROOT_PASSWORD'), `${name} must not receive MySQL root credentials.`);
    requireValue(!(service.volumes ?? []).some(volume => volume.source &&
      mysql.volumes?.some(v => v.target === '/var/run/mysqld' && v.source === volume.source)),
    `${name} must not mount the MySQL socket.`);
  }
  requireValue(mysql.environment?.MYSQL_DATABASE === 'wattharbor' &&
    mysql.environment?.MYSQL_USER === 'wattharbor' && mysql.environment?.MYSQL_ROOT_HOST === 'localhost',
  'MySQL must bootstrap the maintenance account for the unified database with local root access.');
  const maintenance = parseURL(migrate.environment?.DATABASE_URL, 'migrate.DATABASE_URL', 'mysql:');
  requireValue(maintenance.username === 'wattharbor' && maintenance.pathname === '/wattharbor' &&
    maintenance.host === 'mysql:3306', 'Migration must use the maintenance account for mysql/wattharbor.');
  const env = app.environment ?? {};
  requireValue(!Object.hasOwn(env, 'DB_PASSWORD') && !Object.hasOwn(env, 'MYSQL_PASSWORD'),
    'app must not receive maintenance credentials.');
  const runtime = parseURL(env.DATABASE_URL, 'DATABASE_URL', 'mysql:');
  requireValue(runtime.username === 'wattharbor_app' && runtime.pathname === '/wattharbor' && runtime.host === maintenance.host,
    'app must use the runtime account for mysql/wattharbor.');
  for (const key of databaseKeys.slice(1)) {
    requireValue(parseURL(env[key], key, 'mysql:').href === runtime.href, `${key} must match the app database URL.`);
  }
  if (!configOnly) {
    requireValue(decodeURIComponent(maintenance.password) === mysql.environment.MYSQL_PASSWORD,
      'Migration password must match MySQL maintenance credentials.');
    requireValue(decodeURIComponent(runtime.password) === provision.environment.DB_APP_PASSWORD,
      'Runtime database password must match account provisioning.');
    requireValue(mysql.environment.MYSQL_ROOT_PASSWORD === provision.environment.MYSQL_ROOT_PASSWORD,
      'Provisioning root password must match MySQL.');
    requireValue(mysql.environment.MYSQL_PASSWORD !== provision.environment.DB_APP_PASSWORD,
      'Runtime and maintenance database passwords must differ.');
  }
}

function redisArgument(service, key) {
  requireValue(Array.isArray(service.command), 'Redis command must be an argument list.');
  const index = service.command.indexOf(key);
  return index < 0 ? undefined : service.command[index + 1];
}

function validateRedis({ app, cache, stream }, configOnly) {
  for (const [name, service, key, policy, appendOnly] of [
    ['cache', cache, 'REDIS_CACHE_URL', 'allkeys-lru', 'no'],
    ['stream', stream, 'REDIS_STREAM_URL', 'noeviction', 'yes'],
  ]) {
    const url = parseURL(app.environment?.[key], key, 'redis:');
    requireValue(url.host === `${name}:6379`, `${key} must use the ${name} service.`);
    requireValue(redisArgument(service, '--maxmemory-policy') === policy &&
      redisArgument(service, '--appendonly') === appendOnly, `${name} must use ${policy} and appendonly=${appendOnly}.`);
    if (!configOnly) {
      requireValue(decodeURIComponent(url.password) === redisArgument(service, '--requirepass') &&
        decodeURIComponent(url.password) === service.environment?.REDISCLI_AUTH,
      `${name} password must match app and health check configuration.`);
    }
  }
  requireValue(redisArgument(stream, '--appendfsync') === 'everysec' &&
    stream.volumes?.some(volume => volume.type === 'volume' && volume.target === '/data'),
  'stream must fsync every second and store its AOF on a named volume.');
}

function validateCredentials({ app, cache, mysql, 'provision-runtime': provision, stream }, configOnly) {
  const env = app.environment ?? {};
  const paymentMode = env.PAYMENT_MODE || 'disabled';
  const loginMode = env.LOGIN_MODE || 'wechat';
  requireValue(['disabled', 'simulation', 'wechat_direct'].includes(paymentMode), 'app has invalid PAYMENT_MODE.');
  requireValue(['wechat', 'development'].includes(loginMode), 'app has invalid LOGIN_MODE.');
  requireValue(loginMode !== 'development' || paymentMode === 'simulation', 'Development login requires PAYMENT_MODE=simulation.');
  if (configOnly) return;

  const secrets = {
    MYSQL_ROOT_PASSWORD: mysql.environment?.MYSQL_ROOT_PASSWORD,
    DB_PASSWORD: mysql.environment?.MYSQL_PASSWORD,
    DB_APP_PASSWORD: provision.environment?.DB_APP_PASSWORD,
    JWT_SECRET: env.JWT_SECRET,
    SERVICE_TOKEN: env.SERVICE_TOKEN,
    REDIS_PASSWORD: redisArgument(cache, '--requirepass'),
    REDIS_STREAM_PASSWORD: redisArgument(stream, '--requirepass'),
    WECHAT_SECRET: env.WECHAT_SECRET,
    WECHAT_APPID: env.WECHAT_APPID,
  };
  for (const [name, value] of Object.entries(secrets)) {
    requireValue(typeof value === 'string' && value.length > 0 && !value.startsWith('change-me') &&
      !['your_app_secret', 'wx_your_appid'].includes(value), `${name} is missing or still uses a template value.`);
  }
  requireValue(Buffer.byteLength(env.JWT_SECRET, 'utf8') >= 32, 'JWT_SECRET must contain at least 32 bytes.');
  if (paymentMode === 'wechat_direct') {
    for (const key of ['WECHAT_MCH_ID', 'WECHAT_CERT_SERIAL', 'WECHAT_PRIVATE_KEY_PATH', 'WECHAT_NOTIFY_URL']) {
      requireValue(typeof env[key] === 'string' && env[key].length > 0 && env[key] !== 'your_merchant_id',
        `${key} is missing or still uses a template value.`);
    }
    requireValue(typeof env.WECHAT_APIV3_KEY === 'string' && Buffer.byteLength(env.WECHAT_APIV3_KEY, 'utf8') === 32,
      'WECHAT_APIV3_KEY must contain 32 bytes.');
  }
}

function validateProxy({ networks, services }, caddyText) {
  const proxyIP = services.caddy.networks?.edge?.ipv4_address;
  requireValue(networks?.edge?.internal === true && isIP(proxyIP ?? '') === 4,
    'Caddy requires the internal edge network and a fixed IPv4 address.');
  requireValue(services.app.environment?.ADMIN_TRUSTED_PROXIES === proxyIP,
    'app must trust only the fixed Caddy proxy address.');
  requireValue(Object.hasOwn(services.app.networks ?? {}, 'edge') && Object.hasOwn(services.caddy.networks, 'public') &&
    Object.keys(services.caddy.networks).length === 2 && networks?.public && networks.public.internal !== true,
  'Caddy needs separate edge and public networks; app must join edge.');
  for (const [name, service] of Object.entries(services)) {
    requireValue(!Object.hasOwn(service.networks ?? {}, 'edge') || name === 'app' || name === 'caddy',
      'Only app and Caddy may join the edge network.');
    requireValue(!Object.hasOwn(service.networks ?? {}, 'public') || name === 'caddy',
      'Only Caddy may join the public network.');
  }

  requireValue(typeof caddyText === 'string', 'Caddy configuration text is missing.');
  const source = caddyText.replace(/#.*$/gm, '');
  const fields = source.match(/^[ \t]*@public path ([^\r\n]+)$/m)?.[1]?.trim().split(/\s+/) ?? [];
  requireValue(fields.length === publicPaths.length && publicPaths.every(path => fields.includes(path)),
    'Caddy must expose only frontends and public API paths.');
  requireValue(/^\s*handle @public \{\s*reverse_proxy app:8080\s*\}/m.test(source) &&
    (source.match(/\breverse_proxy\b/g)?.length ?? 0) === 1,
  'Caddy must proxy only the public handler to app:8080.');
  requireValue(/^\s*handle \{\s*respond 404\s*\}/m.test(source),
    'Caddy must reject internal APIs, metrics and other paths.');
}

// 输入为 Compose 展开的 JSON 与 Caddyfile 文本。失败消息不得回显凭据。
export function validateDeployment(config, { configOnly = false, caddyText } = {}) {
  const services = config?.services;
  requireValue(services && typeof services === 'object', 'Compose services are missing.');
  const expected = ['app', 'cache', 'caddy', 'migrate', 'mysql', 'provision-runtime', 'stream'];
  for (const name of expected) requireValue(services[name], `Missing service: ${name}.`);
  requireValue(Object.keys(services).length === expected.length, 'Production may only include the single-process deployment services.');
  validatePorts(services);
  validateStartup(services);
  validateDatabase(services, configOnly);
  validateRedis(services, configOnly);
  validateCredentials(services, configOnly);
  validateProxy(config, caddyText);
}

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

export function main(args = process.argv.slice(2)) {
  if (args.some(arg => arg !== '--config-only')) {
    console.error('Usage: node scripts/ops/check-deploy.mjs [--config-only]');
    return 2;
  }
  try {
    const envPath = resolve(root, '.env');
    const envArgs = !process.env.COMPOSE_ENV_FILES && existsSync(envPath) ? ['--env-file', envPath] : [];
    const output = docker(['compose', ...envArgs, '-f', 'docker/compose.prod.yaml', 'config', '--format', 'json'],
      'Compose validation failed. Check .env and run docker compose config --quiet.');
    let config;
    try { config = JSON.parse(output); } catch { throw new Error('Unable to read Compose configuration JSON.'); }
    const caddy = resolve(root, 'docker/Caddyfile');
    validateDeployment(config, { configOnly: args.includes('--config-only'), caddyText: readFileSync(caddy, 'utf8') });
    validatePaths(config);
    console.log('Compose and topology checks passed; validating Caddy configuration...');
    const proxy = config.services.caddy;
    docker(['run', '--rm', '--network', 'none', '-e', `PUBLIC_DOMAIN=${proxy.environment.PUBLIC_DOMAIN}`,
      '-v', `${caddy}:/etc/caddy/Caddyfile:ro`, proxy.image, 'caddy', 'adapt', '--config', '/etc/caddy/Caddyfile', '--validate'],
    'Caddy validation failed. Check Docker image availability and docker/Caddyfile.');
    console.log('Compose, bind paths, ports, database URLs, Redis policies and Caddy validation passed.');
    return 0;
  } catch (error) {
    console.error(error.message);
    return 1;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) process.exitCode = main();
