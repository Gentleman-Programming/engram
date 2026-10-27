import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
const script = fileURLToPath(new URL('./windows-bash-temp-diagnostics.mjs', import.meta.url));
const usage = 'Usage: windows-bash-temp-diagnostics.mjs snapshot FILE | collect BASELINE FILE\n';
function fixture(t) {
  const parent = path.resolve('odd');
  mkdirSync(parent, { recursive: true });
  const root = mkdtempSync(path.join(parent, 'temp-contract-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}
const cli = (args, env = process.env) => spawnSync(process.execPath, [script, ...args], { env, encoding: 'utf8', timeout: 10000 });

test('snapshot CLI records only safe aliases/context, including unset values', t => {
  const root = fixture(t), output = path.join(root, 'baseline.json');
  const result = cli(['snapshot', output], { SystemRoot: process.env.SystemRoot, HOME: root, TEMP: root, GITHUB_SHA: 'revision', GITHUB_TOKEN: 'never-record' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, ''); assert.equal(result.stderr, '');
  const data = JSON.parse(readFileSync(output, 'utf8'));
  assert.equal(data.version, 1); assert.equal(data.aliases.HOME, root);
  assert.equal(data.aliases.TMPDIR, null); assert.equal(data.context.GITHUB_SHA, 'revision');
  assert.doesNotMatch(JSON.stringify(data), /never-record|GITHUB_TOKEN/);
  assert.deepEqual(readdirSync(root), ['baseline.json']);
});

test('CLI help has exact stdout and no stderr', () => {
  const result = cli(['--help']);
  assert.equal(result.status, 0); assert.equal(result.stdout, usage); assert.equal(result.stderr, '');
});

test('CLI rejects invalid arguments without altering existing output or creating scratch', t => {
  const root = fixture(t), output = path.join(root, 'result.json');
  writeFileSync(output, 'sentinel');
  const result = cli(['unknown', output]);
  assert.equal(result.status, 2); assert.equal(result.stdout, ''); assert.equal(result.stderr, usage);
  assert.equal(readFileSync(output, 'utf8'), 'sentinel'); assert.deepEqual(readdirSync(root), ['result.json']);
});

test('representative matrix preserves every child exit and exact streams, environment and cleanup', async t => {
  const { collect, snapshot } = await import('./windows-bash-temp-diagnostics.mjs');
  const root = fixture(t), calls = [];
  const env = { SystemRoot: process.env.SystemRoot, TEMP: root, TMP: root, TMPDIR: root, CODEX_TEST_TRUSTED_BASH: script, SECRET_TOKEN: 'never-record' };
  const baseline = snapshot({ TEMP: 'original-temp' });
  baseline.aliases.BASH_ENV = 'never-execute'; baseline.aliases.SECRET_TOKEN = 'never-record';
  const result = collect(baseline, root, env, (exe, args, options) => {
    calls.push({ exe, args, options });
    return { status: 31, signal: null, stdout: 'raw\r\nstdout\n', stderr: 'bash.exe: warning: could not find /tmp, please create!\r\n' };
  });
  assert.deepEqual(result.cases.map(c => c.name), ['original-aliases', 'suite-aliases', 'fixture-like-no-gate']);
  assert.equal(result.representativeOnly, true); assert.equal(result.goTempDirMeasured, false);
  assert.equal(calls.length, 3);
  for (const [i, c] of result.cases.entries()) {
    assert.equal(c.exit, 31); assert.equal(c.signal, null); assert.equal(c.error, null);
    assert.equal(c.stdout, 'raw\r\nstdout\n'); assert.equal(c.stderr, 'bash.exe: warning: could not find /tmp, please create!\r\n');
    assert.equal(calls[i].options.timeout, 10000);
    assert.deepEqual(calls[i].args.slice(0, 3), ['--noprofile', '--norc', '-c']);
    assert.equal(calls[i].options.env.BASH_ENV, ''); assert.equal(calls[i].options.env.ENV, '');
    assert.doesNotMatch(JSON.stringify(c.environment), /SECRET_TOKEN|never-record/);
  }
  assert.doesNotMatch(JSON.stringify(result), /never-record|never-execute|SECRET_TOKEN/);
  assert.equal(calls[0].options.env.TEMP, 'original-temp');
  assert.equal(Object.hasOwn(calls[0].options.env, 'TMPDIR'), false);
  assert.equal(calls[1].options.env.TEMP, root);
  assert.equal(calls[2].options.env.PATH, ''); assert.equal(calls[2].options.env.MSYS_NO_PATHCONV, '1');
  assert.equal(result.scratchRemoved, true); assert.equal(existsSync(result.scratch), false);
  assert.deepEqual(readdirSync(root), []);
});

test('timeout retains partial stdout/stderr and cleans private scratch', async t => {
  const { collect, snapshot } = await import('./windows-bash-temp-diagnostics.mjs');
  const root = fixture(t);
  const result = collect(snapshot({}), root, { CODEX_TEST_TRUSTED_BASH: script }, () => ({ status: null, signal: 'SIGTERM', error: new Error('ETIMEDOUT'), stdout: 'partial', stderr: 'original error' }));
  assert.equal(result.cases.length, 3);
  for (const c of result.cases) {
    assert.equal(c.exit, null); assert.equal(c.signal, 'SIGTERM'); assert.equal(c.error, 'ETIMEDOUT');
    assert.equal(c.stdout, 'partial'); assert.equal(c.stderr, 'original error');
  }
  assert.equal(result.scratchRemoved, true); assert.deepEqual(readdirSync(root), []);
});

test('profile setup error retains earlier observations without repairing missing parents', async t => {
  const { collect, snapshot } = await import('./windows-bash-temp-diagnostics.mjs');
  const root = fixture(t), missing = path.join(root, 'missing');
  const result = collect(snapshot({}), missing, { CODEX_TEST_TRUSTED_BASH: script }, () => ({ status: 0, signal: null, stdout: 'recorded', stderr: 'warning' }));
  assert.equal(result.cases.length, 2); assert.match(result.setupError, /ENOENT/);
  assert.equal(result.cases[0].stdout, 'recorded'); assert.equal(result.cases[1].stderr, 'warning');
  assert.equal(result.scratchRemoved, true); assert.equal(existsSync(missing), false);
  assert.deepEqual(readdirSync(root), []);
});

test('CLI setup failure leaves existing output and directory contents unchanged', t => {
  const root = fixture(t), baseline = path.join(root, 'invalid.json'), output = path.join(root, 'result.json');
  writeFileSync(baseline, '{}'); writeFileSync(output, 'sentinel');
  const result = cli(['collect', baseline, output]);
  assert.equal(result.status, 1); assert.equal(result.stdout, '');
  assert.equal(result.stderr, 'Invalid diagnostic baseline\n');
  assert.equal(readFileSync(output, 'utf8'), 'sentinel');
  assert.deepEqual(readdirSync(root).sort(), ['invalid.json', 'result.json']);
});
