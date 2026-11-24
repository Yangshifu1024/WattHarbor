import assert from 'node:assert/strict';
import test from 'node:test';
import { buildProvisionSQL, main } from './provision-runtime.mjs';

const env = { MYSQL_ROOT_PASSWORD: 'fixture-root', DB_APP_PASSWORD: 'fixture-app' };

test('password quoting preserves quotes, slashes and newlines inside SQL literal', () => {
  const sql = buildProvisionSQL({ ...env, DB_APP_PASSWORD: "x'\\\ny; GRANT ALL ON *.* TO 'evil'@'%';" }, '--unified');
  assert.ok(sql.startsWith("SET SESSION sql_mode='NO_BACKSLASH_ESCAPES';\n"));
  assert.ok(sql.includes("IDENTIFIED BY 'x''\\\ny; GRANT ALL ON *.* TO ''evil''@''%'';';"));
});

test('missing and NUL credentials fail before client execution without leaking values', () => {
  assert.throws(() => buildProvisionSQL({ ...env, DB_APP_PASSWORD: '' }, '--unified'), /DB_APP_PASSWORD is required/);
  assert.throws(() => buildProvisionSQL({ ...env, DB_APP_PASSWORD: 'secret\0value' }, '--unified'), { message: 'DB_APP_PASSWORD is required and must not contain NUL' });
});

for (const [layout, schemas] of [
  ['--unified', ['wattharbor']],
  ['--three-schema', ['central\\_db', 'gateway\\_db', 'worker\\_db']],
]) {
  test(`${layout} grants only schema-local DML to the single runtime account`, () => {
    const grants = buildProvisionSQL(env, layout).split('\n').filter(row => row.startsWith('GRANT '));
    assert.deepEqual(grants, schemas.map(schema =>
      `GRANT SELECT, INSERT, UPDATE, DELETE ON \`${schema}\`.* TO 'wattharbor_app'@'%';`));
  });
}

test('unknown arguments return usage without executing MySQL', () => {
  const previous = console.error;
  const messages = [];
  console.error = message => messages.push(message);
  try {
    assert.equal(main(['--unexpected'], env), 2);
    assert.deepEqual(messages, ['Usage: node scripts/db/provision-runtime.mjs --unified|--three-schema']);
  } finally { console.error = previous; }
});
