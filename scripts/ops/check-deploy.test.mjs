import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { main, validateDeployment } from './check-deploy.mjs';

const caddyText = readFileSync(new URL('../../deploy/Caddyfile', import.meta.url), 'utf8').replace(/\r\n/g, '\n');

function configuration() {
  const runtime = 'mysql://wattharbor_app:fixture-app-password@mysql:3306/wattharbor';
  const rootPassword = 'fixture-root-password';
  const socket = { type: 'volume', source: 'mysql-socket', target: '/var/run/mysqld' };
  const fixturePort = port => ({ target: port, published: String(port), protocol: 'tcp' });
  const healthy = { condition: 'service_healthy' };
  const completed = { condition: 'service_completed_successfully' };
  return {
    services: {
      mysql: {
        environment: {
          MYSQL_ROOT_PASSWORD: rootPassword, MYSQL_ROOT_HOST: 'localhost',
          MYSQL_DATABASE: 'wattharbor', MYSQL_USER: 'wattharbor', MYSQL_PASSWORD: 'fixture-migrate-password',
        },
        volumes: [socket],
      },
      migrate: {
        image: 'fixture-monolith',
        command: ['/usr/local/bin/migrate', '-schema', 'unified'],
        environment: { DATABASE_URL: 'mysql://wattharbor:fixture-migrate-password@mysql:3306/wattharbor' },
        depends_on: { mysql: healthy },
      },
      'provision-runtime': {
        command: ['--unified'],
        network_mode: 'none',
        environment: { MYSQL_ROOT_PASSWORD: rootPassword, DB_APP_PASSWORD: 'fixture-app-password', MYSQL_SOCKET: '/var/run/mysqld/mysqld.sock' },
        volumes: [{ ...socket, read_only: true }],
        depends_on: { migrate: completed },
      },
      app: {
        image: 'fixture-monolith',
        environment: {
          APP_UNIFIED_DATABASE: 'true',
          DATABASE_URL: runtime,
          DATABASE_URL_CENTRAL: runtime,
          DATABASE_URL_GATEWAY: runtime,
          DATABASE_URL_WORKER: runtime,
          JWT_SECRET: 'fixture-session-secret-at-least-32-bytes',
          SERVICE_TOKEN: 'fixture-service-token',
          WECHAT_APPID: 'wx_fixture',
          WECHAT_SECRET: 'fixture-login-secret',
          REDIS_CACHE_URL: 'redis://:fixture-cache-password@cache:6379/0',
          REDIS_STREAM_URL: 'redis://:fixture-stream-password@stream:6379/0',
          ADMIN_TRUSTED_PROXIES: '172.30.82.2',
        },
        depends_on: { 'provision-runtime': completed, cache: healthy, stream: healthy },
        healthcheck: { test: ['CMD', 'wget', '-qO', '/dev/null', 'http://127.0.0.1:8080/health/ready'] },
        ports: [fixturePort(9100)],
        networks: { default: null, edge: null },
      },
      cache: {
        environment: { REDISCLI_AUTH: 'fixture-cache-password' },
        command: ['redis-server', '--appendonly', 'no', '--maxmemory-policy', 'allkeys-lru', '--requirepass', 'fixture-cache-password'],
        healthcheck: { test: ['CMD', 'redis-cli', 'ping'] },
      },
      stream: {
        environment: { REDISCLI_AUTH: 'fixture-stream-password' },
        command: ['redis-server', '--appendonly', 'yes', '--appendfsync', 'everysec', '--maxmemory-policy', 'noeviction', '--requirepass', 'fixture-stream-password'],
        healthcheck: { test: ['CMD', 'redis-cli', 'ping'] },
        volumes: [{ type: 'volume', source: 'monolith-stream', target: '/data' }],
      },
      caddy: {
        environment: { PUBLIC_DOMAIN: 'fixture.example' },
        ports: [fixturePort(80), fixturePort(443)],
        networks: { edge: { ipv4_address: '172.30.82.2' }, public: {} },
        depends_on: { app: healthy },
      },
    },
    networks: { edge: { internal: true }, public: {} },
  };
}

function validate(value, options = {}) {
  return validateDeployment(value, { caddyText, ...options });
}

test('canonical monolith topology passes', () => {
  assert.doesNotThrow(() => validate(configuration()));
});

for (const [label, change, message] of [
  ['missing app', value => { delete value.services.app; }, 'Missing service: app.'],
  ['extra legacy process', value => { value.services['wattharbor-worker'] = {}; }, 'Production may only include the single-process deployment services.'],
  ['app HTTP exposure', value => { value.services.app.ports.push({ target: 8080, published: '8080' }); }, 'Unexpected published ports on app.'],
  ['storage exposed', value => { value.services.mysql.ports = [{ target: 3306, published: '3306' }]; }, 'Unexpected published ports on mysql.'],
  ['monolith on three schemas', value => { value.services.app.environment.APP_UNIFIED_DATABASE = 'false'; }, 'app must use the unified database.'],
  ['migration skipped', value => { delete value.services['provision-runtime'].depends_on.migrate; }, 'Provisioning must finish after migrations and before app starts.'],
  ['provisioning skipped', value => { delete value.services.app.depends_on['provision-runtime']; }, 'Provisioning must finish after migrations and before app starts.'],
  ['provisioning with network access', value => { delete value.services['provision-runtime'].network_mode; }, 'provision-runtime must use unified mode without network access.'],
  ['provisioning with invalid socket', value => { value.services['provision-runtime'].volumes[0].read_only = false; }, 'provision-runtime must use the shared MySQL socket mounted read-only.'],
  ['Caddy starts before readiness', value => { value.services.caddy.depends_on.app = { condition: 'service_started' }; }, 'Caddy must wait for app readiness.'],
  ['root credentials in app', value => { value.services.app.environment.MYSQL_ROOT_PASSWORD = 'fixture-sensitive-root'; }, 'app must not receive MySQL root credentials.'],
  ['migration credentials in app', value => { value.services.app.environment.DATABASE_URL = value.services.migrate.environment.DATABASE_URL; }, 'app must use the runtime account for mysql/wattharbor.'],
  ['one role with a different schema', value => { value.services.app.environment.DATABASE_URL_GATEWAY += '_other'; }, 'DATABASE_URL_GATEWAY must match the app database URL.'],
  ['cache redirected to stream', value => { value.services.app.environment.REDIS_CACHE_URL = value.services.app.environment.REDIS_STREAM_URL; }, 'REDIS_CACHE_URL must use the cache service.'],
  ['evictable event stream', value => { value.services.stream.command[6] = 'allkeys-lru'; }, 'stream must use noeviction and appendonly=yes.'],
  ['ephemeral event stream', value => { delete value.services.stream.volumes; }, 'stream must fsync every second and store its AOF on a named volume.'],
  ['public proxy network', value => { value.networks.edge.internal = false; }, 'Caddy requires the internal edge network and a fixed IPv4 address.'],
  ['entire subnet trust', value => { value.services.app.environment.ADMIN_TRUSTED_PROXIES = '172.30.82.0/24'; }, 'app must trust only the fixed Caddy proxy address.'],
  ['unexpected edge member', value => { value.services.stream.networks = { edge: {} }; }, 'Only app and Caddy may join the edge network.'],
  ['Caddy connected to data network', value => { value.services.caddy.networks.default = {}; }, 'Caddy needs separate edge and public networks; app must join edge.'],
]) {
  test(`deployment rejects ${label}`, () => {
    const value = configuration();
    change(value);
    assert.throws(() => validate(value, { configOnly: true }), { message });
  });
}

test('runtime credential mismatch does not expose fixture secrets', () => {
  const value = configuration();
  value.services['provision-runtime'].environment.DB_APP_PASSWORD = 'fixture-sensitive-wrong-password';
  assert.throws(() => validate(value), { message: 'Runtime database password must match account provisioning.' });
  assert.doesNotThrow(() => validate(value, { configOnly: true }));
});

test('checker validates production JWT and template WeChat credentials', () => {
  const value = configuration();
  value.services.app.environment.JWT_SECRET = 'fixture-too-short';
  assert.throws(() => validate(value), { message: 'JWT_SECRET must contain at least 32 bytes.' });
  value.services.app.environment.JWT_SECRET = 'fixture-session-secret-at-least-32-bytes';
  value.services.app.environment.WECHAT_SECRET = 'your_app_secret';
  assert.throws(() => validate(value), { message: 'WECHAT_SECRET is missing or still uses a template value.' });
  assert.doesNotThrow(() => validate(value, { configOnly: true }));
});

test('development login still requires simulation', () => {
  const value = configuration();
  value.services.app.environment.LOGIN_MODE = 'development';
  assert.throws(() => validate(value, { configOnly: true }), { message: 'Development login requires PAYMENT_MODE=simulation.' });
  value.services.app.environment.PAYMENT_MODE = 'simulation';
  assert.doesNotThrow(() => validate(value));
});

test('real WeChat payment requires credentials and the API key byte count', () => {
  const value = configuration();
  const env = value.services.app.environment;
  Object.assign(env, {
    PAYMENT_MODE: 'wechat_direct', WECHAT_MCH_ID: 'fixture-merchant', WECHAT_CERT_SERIAL: 'fixture-cert',
    WECHAT_APIV3_KEY: '12345678901234567890123456789012', WECHAT_PRIVATE_KEY_PATH: '/run/secrets/fixture.pem',
    WECHAT_NOTIFY_URL: 'https://fixture.example/api/v1/public/payments/wechat/callback',
  });
  assert.doesNotThrow(() => validate(value));
  env.WECHAT_APIV3_KEY = '密'.repeat(32);
  assert.throws(() => validate(value), { message: 'WECHAT_APIV3_KEY must contain 32 bytes.' });
  delete env.WECHAT_PRIVATE_KEY_PATH;
  assert.throws(() => validate(value), { message: 'WECHAT_PRIVATE_KEY_PATH is missing or still uses a template value.' });
});

test('Caddy refuses internal APIs and metrics', () => {
  for (const suffix of ['/api/v1/internal/*', '/metrics*']) {
    const unsafe = caddyText.replace('/api/v1/public/*', `/api/v1/public/* ${suffix}`);
    assert.throws(() => validate(configuration(), { caddyText: unsafe }), { message: 'Caddy must expose only frontends and public API paths.' });
  }
  const missingFallback = caddyText.replace('respond 404', 'respond 200');
  assert.throws(() => validate(configuration(), { caddyText: missingFallback }), { message: 'Caddy must reject internal APIs, metrics and other paths.' });
});

test('Caddy proxies the allowlist only to the monolith', () => {
  const changed = caddyText.replace('app:8080', 'wattharbor-user:8080');
  assert.throws(() => validate(configuration(), { caddyText: changed }), { message: 'Caddy must proxy only the public handler to app:8080.' });
});

test('unknown CLI argument returns usage without executing Docker', () => {
  const errors = [];
  const previous = console.error;
  console.error = message => errors.push(message);
  try {
    assert.equal(main(['--invalid-fixture-option']), 2);
    assert.deepEqual(errors, ['Usage: node scripts/ops/check-deploy.mjs [--config-only]']);
  } finally {
    console.error = previous;
  }
});
