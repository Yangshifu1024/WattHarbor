import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import test from 'node:test';
import { main, validateDeployment } from './check-deploy.mjs';

const caddyText = readFileSync(resolve('docker/Caddyfile'), 'utf8').replace(/\r\n/g, '\n');

// configuration 返回按五服务生产配置构造的独立 JSON 夹具，只有测试值且不读取 .env、不连接任何服务。
function configuration() {
  const centralURL = 'mysql://fixture:fixture-database-password@mysql:3306/central_db';
  const gatewayURL = 'mysql://fixture:fixture-database-password@mysql:3306/gateway_db';
  const workerURL = 'mysql://fixture:fixture-database-password@mysql:3306/worker_db';
  const token = 'fixture-shared-service-token';
  const jwt = 'fixture-session-secret-at-least-32-characters';
  const value = {
    services: {
      'chargepilot-mysql': { environment: { MYSQL_ROOT_PASSWORD: 'fixture-root-password', MYSQL_PASSWORD: 'fixture-database-password' } },
      'chargepilot-user': { environment: { DATABASE_URL_CENTRAL: centralURL, REDIS_CACHE_URL: 'redis://cache:6379/0', JWT_SECRET: jwt, SERVICE_TOKEN: token, WECHAT_APPID: 'wx_fixture', WECHAT_SECRET: 'fixture-login-secret' } },
      'chargepilot-admin': { environment: { DATABASE_URL_CENTRAL: centralURL, REDIS_CACHE_URL: 'redis://cache:6379/0', JWT_SECRET: jwt, SERVICE_TOKEN: token } },
      'chargepilot-central': { environment: { DATABASE_URL_CENTRAL: centralURL, REDIS_CACHE_URL: 'redis://cache:6379/0', SERVICE_TOKEN: token, WECHAT_APPID: 'wx_fixture' }, expose: ['8082'] },
      'chargepilot-gateway': { environment: { DATABASE_URL: gatewayURL, SERVICE_TOKEN: token }, ports: [{ target: 9100, published: '9100' }] },
      'chargepilot-worker': { environment: { DATABASE_URL_CENTRAL: centralURL, DATABASE_URL_GATEWAY: gatewayURL, DATABASE_URL_WORKER: workerURL, REDIS_STREAM_URL: 'redis://stream:6379/0', SERVICE_TOKEN: token } },
      'chargepilot-migrate': { environment: { DATABASE_URL_CENTRAL: centralURL, DATABASE_URL_GATEWAY: gatewayURL, DATABASE_URL_WORKER: workerURL } },
      'chargepilot-caddy': { image: 'fixture-caddy', environment: { PUBLIC_DOMAIN: 'fixture.example' }, ports: [{ target: 80, published: '80' }, { target: 443, published: '443' }] },
      'chargepilot-redis-cache': { command: ['redis-server', '--maxmemory-policy', 'allkeys-lru', '--requirepass', 'fixture-cache-password'] },
      'chargepilot-redis-stream': { command: ['redis-server', '--maxmemory-policy', 'noeviction', '--appendonly', 'yes', '--requirepass', 'fixture-stream-password'] },
    },
  };
  const provision = { environment: {}, depends_on: { 'chargepilot-migrate': { condition: 'service_completed_successfully' } } };
  value.services['chargepilot-provision-runtime'] = provision;
  for (const name of ['user','admin','central','gateway','worker']) {
    const service = value.services[`chargepilot-${name}`];
    const password = `fixture-${name}-database-password`;
    provision.environment[`DB_${name.toUpperCase()}_PASSWORD`] = password;
    service.depends_on = { 'chargepilot-provision-runtime': { condition: 'service_completed_successfully' } };
    for (const key of Object.keys(service.environment).filter(key => key.startsWith('DATABASE_URL'))) {
      const url = new URL(service.environment[key]);
      url.username = `chargepilot_${name}`;
      url.password = password;
      service.environment[key] = url.href;
    }
  }
  for (const key of Object.keys(value.services['chargepilot-migrate'].environment)) {
    value.services['chargepilot-migrate'].environment[key] = value.services['chargepilot-migrate'].environment[key].replace('fixture:', 'chargepilot:');
  }
  return value;
}

// validate 将 value 与当前真实 Caddy 文本交给正式纯校验函数；options 只覆盖显式子测试选项，无 IO 副作用。
function validate(value, options = {}) {
  return validateDeployment(value, { caddyText, ...options });
}

test('canonical five services pass without central JWT, login secret or stream', () => {
  assert.doesNotThrow(() => validate(configuration()));
});

for (const name of ['user', 'admin', 'central', 'gateway', 'worker']) {
  test(`missing ${name} fails with its service name`, () => {
    const value = configuration();
    delete value.services[`chargepilot-${name}`];
    assert.throws(() => validate(value), { message: `Missing service: chargepilot-${name}.` });
  });
}

for (const key of ['JWT_SECRET', 'LOGIN_MODE', 'WECHAT_SECRET', 'REDIS_STREAM_URL', 'ADMIN_BOOTSTRAP_USER']) {
  test(`central must not receive ${key}`, () => {
    const value = configuration();
    value.services['chargepilot-central'].environment[key] = 'unused-fixture-value';
    assert.throws(() => validate(value, { configOnly: true }), { message: `central must not receive ${key}.` });
  });
}

for (const name of ['user', 'admin']) {
  test(`${name} keeps JWT minimum length and rejects missing/default values`, () => {
    const value = configuration();
    const environment = value.services[`chargepilot-${name}`].environment;
    environment.JWT_SECRET = 'fixture-too-short';
    assert.throws(() => validate(value), { message: `${name}.JWT_SECRET must contain at least 32 characters.` });
    environment.JWT_SECRET = 'change-me-fixture-template-secret';
    assert.throws(() => validate(value), { message: `${name}.JWT_SECRET is missing or still uses a template value.` });
    delete environment.JWT_SECRET;
    assert.throws(() => validate(value), { message: `${name}.JWT_SECRET is missing or still uses a template value.` });
  });
}

test('config-only skips credential values and lengths while preserving topology', () => {
  const value = configuration();
  value.services['chargepilot-user'].environment.JWT_SECRET = 'change-me';
  delete value.services['chargepilot-admin'].environment.JWT_SECRET;
  value.services['chargepilot-user'].environment.WECHAT_SECRET = 'your_app_secret';
  value.services['chargepilot-worker'].environment.SERVICE_TOKEN = 'different-token';
  assert.doesNotThrow(() => validate(value, { configOnly: true }));
  value.services['chargepilot-central'].ports = [{ target: 8082, published: '8082' }];
  assert.throws(() => validate(value, { configOnly: true }), { message: 'Unexpected published port on chargepilot-central.' });
});

for (const name of ['user', 'admin', 'central', 'worker']) {
  test(`${name} HTTP port must stay unpublished`, () => {
    const value = configuration();
    value.services[`chargepilot-${name}`].ports = [{ target: 8082, published: '18082', host_ip: '127.0.0.1' }];
    assert.throws(() => validate(value), { message: `Unexpected published port on chargepilot-${name}.` });
  });
}

test('only original Caddy and gateway port mappings pass', () => {
  const value = configuration();
  value.services['chargepilot-gateway'].ports.push({ target: 8083, published: '8083' });
  assert.throws(() => validate(value), { message: 'Unexpected published port on chargepilot-gateway.' });
  value.services['chargepilot-gateway'].ports.pop();
  value.services['chargepilot-caddy'].ports[0].published = '18080';
  assert.throws(() => validate(value), { message: 'Unexpected published port on chargepilot-caddy.' });
});

for (const [name, key, schema] of [['user', 'DATABASE_URL_CENTRAL', 'central_db'], ['admin', 'DATABASE_URL_CENTRAL', 'central_db'], ['worker', 'DATABASE_URL_GATEWAY', 'gateway_db'], ['worker', 'DATABASE_URL_WORKER', 'worker_db'], ['gateway', 'DATABASE_URL', 'gateway_db'], ['migrate', 'DATABASE_URL_WORKER', 'worker_db']]) {
  test(`${name}/${key} keeps its schema`, () => {
    const value = configuration();
    value.services[`chargepilot-${name}`].environment[key] = 'mysql://fixture:fixture@mysql:3306/wrong_db';
    assert.throws(() => validate(value), { message: `${key} must target ${schema}.` });
  });
}

test('shared central URL cannot silently point user at another database host', () => {
  const value = configuration();
  value.services['chargepilot-user'].environment.DATABASE_URL_CENTRAL = 'mysql://fixture:fixture-database-password@other:3306/central_db';
  assert.throws(() => validate(value), { message: 'user must use the shared central database URL.' });
});

test('obsolete central aliases remain rejected', () => {
  const value = configuration();
  value.services['chargepilot-central'].environment.DATABASE_URL_USER = 'unused-fixture';
  assert.throws(() => validate(value), { message: 'Central must use one database URL.' });
});

test('runtime account and provisioning dependency cannot be bypassed', () => {
  const value = configuration();
  const service = value.services['chargepilot-worker'];
  const original = service.environment.DATABASE_URL_CENTRAL;
  service.environment.DATABASE_URL_CENTRAL = original.replace('chargepilot_worker:', 'chargepilot:');
  assert.throws(() => validate(value), { message: 'worker must use its own runtime database account.' });
  service.environment.DATABASE_URL_CENTRAL = original;
  delete service.depends_on;
  assert.throws(() => validate(value), { message: 'worker must wait for runtime accounts.' });
});

test('mismatched runtime password is rejected without exposing it', () => {
  const value = configuration();
  value.services['chargepilot-provision-runtime'].environment.DB_USER_PASSWORD = 'fixture-sensitive-wrong-password';
  assert.throws(() => validate(value), { message: 'user database password must match account provisioning.' });
});

test('Redis policies retain cache eviction and durable stream noeviction', () => {
  for (const name of ['redis-cache', 'redis-stream']) {
    const value = configuration();
    value.services[`chargepilot-${name}`].command[2] = 'wrong-policy';
    assert.throws(() => validate(value), new RegExp(`${name} must use`));
  }
});

test('all application tokens must be present and equal without leaking their values', () => {
  const value = configuration();
  value.services['chargepilot-worker'].environment.SERVICE_TOKEN = 'fixture-sensitive-token-should-never-appear';
  assert.throws(() => validate(value), { message: 'worker must use the shared SERVICE_TOKEN.' });
  delete value.services['chargepilot-worker'].environment.SERVICE_TOKEN;
  assert.throws(() => validate(value), { message: 'worker.SERVICE_TOKEN is missing or still uses a template value.' });
});

test('user login secret and payment application IDs reject template values', () => {
  const value = configuration();
  value.services['chargepilot-user'].environment.WECHAT_SECRET = 'your_app_secret';
  assert.throws(() => validate(value), { message: 'user.WECHAT_SECRET is missing or still uses a template value.' });
  value.services['chargepilot-user'].environment.WECHAT_SECRET = 'fixture-secret';
  value.services['chargepilot-central'].environment.WECHAT_APPID = 'wx_your_appid';
  assert.throws(() => validate(value), { message: 'central.WECHAT_APPID is missing or still uses a template value.' });
});

test('absent payment mode keeps disabled and an explicit mismatch fails', () => {
  const value = configuration();
  assert.doesNotThrow(() => validate(value));
  value.services['chargepilot-user'].environment.PAYMENT_MODE = 'simulation';
  assert.throws(() => validate(value), { message: 'user and central must use the same PAYMENT_MODE.' });
  value.services['chargepilot-central'].environment.PAYMENT_MODE = 'simulation';
  value.services['chargepilot-user'].environment.LOGIN_MODE = 'development';
  assert.doesNotThrow(() => validate(value));
  value.services['chargepilot-central'].environment.PAYMENT_MODE = 'invalid';
  assert.throws(() => validate(value), { message: 'central has invalid PAYMENT_MODE.' });
});

test('user development login still requires simulation', () => {
  const value = configuration();
  value.services['chargepilot-user'].environment.LOGIN_MODE = 'development';
  assert.throws(() => validate(value), { message: 'Development login requires PAYMENT_MODE=simulation.' });
});

test('direct payment requires credentials in both real consumers without opening key files', () => {
  const value = configuration();
  for (const name of ['user', 'central']) {
    Object.assign(value.services[`chargepilot-${name}`].environment, {
      PAYMENT_MODE: 'wechat_direct', WECHAT_MCH_ID: 'fixture-merchant', WECHAT_CERT_SERIAL: 'fixture-cert',
      WECHAT_APIV3_KEY: '12345678901234567890123456789012', WECHAT_PRIVATE_KEY_PATH: '/run/secrets/fixture.pem',
      WECHAT_NOTIFY_URL: 'https://fixture.example/api/v1/public/payments/wechat/callback',
    });
  }
  assert.doesNotThrow(() => validate(value));
  value.services['chargepilot-central'].environment.WECHAT_APIV3_KEY = '密'.repeat(32);
  assert.throws(() => validate(value), { message: 'central.WECHAT_APIV3_KEY must contain 32 bytes.' });
  value.services['chargepilot-central'].environment.WECHAT_APIV3_KEY = '12345678901234567890123456789012';
  delete value.services['chargepilot-central'].environment.WECHAT_PRIVATE_KEY_PATH;
  assert.throws(() => validate(value), { message: 'central.WECHAT_PRIVATE_KEY_PATH is missing or still uses a template value.' });
});

test('Caddy internal handler must actually reject requests', () => {
  const unsafe = caddyText.replace('handle /api/v1/internal/* {\n        respond 404', 'handle /api/v1/internal/* {\n        reverse_proxy chargepilot-central:8082');
  assert.notEqual(unsafe, caddyText);
  assert.throws(() => validate(configuration(), { caddyText: unsafe }), { message: 'Caddy must block the internal API routes.' });
});

test('Caddy must route user and admin to their own deployed services', () => {
  for (const [destination, path] of [['chargepilot-admin:8081', '/api/v1/admin/*'], ['chargepilot-user:8080', '/api/v1/user/*']]) {
    const wrong = caddyText.replace(destination, 'chargepilot-central:8082');
    assert.throws(() => validate(configuration(), { caddyText: wrong }), { message: `Caddy must route ${path} to ${destination}.` });
  }
});

test('unknown CLI argument returns original usage exit code without executing Docker', () => {
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
