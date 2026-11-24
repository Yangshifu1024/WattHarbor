import assert from 'node:assert/strict';
import test from 'node:test';
import { buildProvisionSQL, main } from './provision-runtime.mjs';

const env = { MYSQL_ROOT_PASSWORD: 'fixture-root' };
for (const name of ['USER', 'ADMIN', 'CENTRAL', 'GATEWAY', 'WORKER']) env[`DB_${name}_PASSWORD`] = `fixture-${name}`;

test('password quoting preserves quotes, slashes and newlines inside SQL literal', () => {
  const sql = buildProvisionSQL({ ...env, DB_USER_PASSWORD: "x'\\\ny; GRANT ALL ON *.* TO 'evil'@'%';" });
  assert.ok(sql.startsWith("SET SESSION sql_mode='NO_BACKSLASH_ESCAPES';\n"));
  assert.ok(sql.includes("IDENTIFIED BY 'x''\\\ny; GRANT ALL ON *.* TO ''evil''@''%'';';"));
});

test('missing and NUL credentials fail before client execution without leaking values', () => {
  assert.throws(() => buildProvisionSQL({ ...env, DB_WORKER_PASSWORD: '' }), /DB_WORKER_PASSWORD is required/);
  assert.throws(() => buildProvisionSQL({ ...env, DB_USER_PASSWORD: 'secret\0value' }), { message: 'DB_USER_PASSWORD is required and must not contain NUL' });
});

test('runtime grants remain bounded and worker cross-schema rights are only SELECT UPDATE', () => {
  const grants = buildProvisionSQL(env).split('\n').filter(row => row.startsWith('GRANT '));
  assert.equal(grants.length, 8);
  for (const row of grants) assert.doesNotMatch(row, /ALL PRIVILEGES|CREATE|ALTER|DROP|GRANT OPTION|ON \*\.\*/);
  assert.equal(grants.filter(row => row.endsWith("TO 'wattharbor_worker'@'%';")).length, 4);
  for (const row of grants.filter(row => /ON (central|gateway)_db\./.test(row) && row.includes('wattharbor_worker'))) {
    assert.match(row, /^GRANT SELECT, UPDATE ON /);
  }
});

test('unknown arguments return usage without executing MySQL', () => {
  const previous = console.error;
  const messages = [];
  console.error = message => messages.push(message);
  try {
    assert.equal(main(['--unexpected'], env), 2);
    assert.deepEqual(messages, ['Usage: node scripts/db/provision-runtime.mjs']);
  } finally { console.error = previous; }
});
