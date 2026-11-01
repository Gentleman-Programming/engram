import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const prefix = 'github.com/Gentleman-Programming/engram/v3/';
const profiles = {
  'postgres-measurements': [
    ...['chunks', 'mutations'].flatMap(axis => [10, 100, 1000].map(n =>
      ['internal/cloud/cloudstore', `TestDashboardReadModelPostgresCounts/${axis}/H${n}`])),
    ['internal/cloud/cloudstore', 'TestDashboardReadModelPostgresControlsAndRecreation'],
    ['internal/cloud/cloudserver', 'TestPausePGPullSnapshotPagination'],
    ...[1, 10, 100].map(n => ['internal/cloud/cloudserver', `TestMutationPausePostgresQueryCount/B100/D${n}`]),
  ],
  'windows-recovery': [['cmd/engram', 'TestHookManagedLocalPublicRecovery']],
  'unsupported-node': [['tools/release-candidate', 'TestArtifactCoreUnsupportedNode']],
};
const checker = new URL('./prerequisite-coverage.mjs', import.meta.url);
function passing(profile) {
  const packages = [...new Set(profiles[profile].map(([pkg]) => prefix + pkg))];
  return [
    ...packages.map(Package => ({ Action: 'start', Package })),
    ...profiles[profile].flatMap(([pkg, Test]) => ['run', 'pass'].map(Action => ({ Action, Package: prefix + pkg, Test }))),
    ...packages.map(Package => ({ Action: 'pass', Package })),
  ];
}
function check(profile, events, exit = '0') {
  const dir = mkdtempSync(join(tmpdir(), 'engram-coverage-'));
  try {
    const file = join(dir, 'events.json');
    const bytes = events.map(e => typeof e === 'string' ? e : JSON.stringify(e)).join('\n') + '\n';
    writeFileSync(file, bytes);
    const result = spawnSync(process.execPath, [fileURLToPath(checker), profile, file, exit], { encoding: 'utf8' });
    assert.equal(readFileSync(file, 'utf8'), bytes, 'evidence remains unchanged');
    return result;
  } finally { rmSync(dir, { recursive: true, force: true }); }
}
for (const [profile, cases] of Object.entries(profiles)) {
  test(`${profile}: every required case and package passed`, () => {
    const result = check(profile, passing(profile));
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, `Coverage evidence verified: ${profile}, ${cases.length} required cases passed.\n`);
    assert.equal(result.stderr, '');
  });
  for (const [pkg, name] of cases) {
    test(`${profile}: rejects missing ${name}`, () => {
      const result = check(profile, passing(profile).filter(e => !(e.Package === prefix + pkg && e.Test === name)));
      assert.equal(result.status, 1);
      assert.equal(result.stdout, '');
      assert.match(result.stderr, /^Coverage evidence rejected:/);
    });
  }
  for (const kind of ['skip', 'fail', 'missing-run', 'duplicate', 'unfinished', 'malformed', 'nonzero']) {
    test(`${profile}: rejects ${kind} evidence`, () => {
      let events = passing(profile);
      const terminal = events.find(e => e.Test && e.Action === 'pass');
      if (kind === 'skip' || kind === 'fail') terminal.Action = kind;
      if (kind === 'missing-run') events = events.filter(e => !(e.Test === terminal.Test && e.Action === 'run'));
      if (kind === 'duplicate') events.splice(events.indexOf(terminal), 0, { ...terminal });
      if (kind === 'unfinished') events = events.filter(e => e !== terminal);
      if (kind === 'malformed') events.push('not json');
      const result = check(profile, events, kind === 'nonzero' ? '1' : '0');
      assert.equal(result.status, 1);
      assert.equal(result.stdout, '');
      assert.match(result.stderr, /^Coverage evidence rejected:/);
    });
  }
}

test('measurement lane preserves PG16 and wires owned PG18.6 fixtures', () => {
  const ci = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8');
  assert.ok(/run: bash \.github\/scripts\/postgres-measurements\.sh/.test(ci), 'owned measurement lane');
  assert.match(ci, /node --test \.github\/scripts\/prerequisite-coverage\.test\.mjs/);
  assert.match(ci, /image: postgres:16-alpine/);
  assert.match(ci, /go test -json -count=1 -timeout=10m \.\/internal\/cloud\/cloudstore \.\/internal\/cloud\/cloudserver \.\/cmd\/engram/);
  const script = readFileSync(new URL('./postgres-measurements.sh', import.meta.url), 'utf8');
  for (const token of ['--publish "127.0.0.1:$port:5432"', 'start engram1832 15432', 'start engram1831 15431',
    'shared_preload_libraries=pg_stat_statements', 'compute_query_id=on',
    'CREATE EXTENSION pg_stat_statements', 'unset CLOUDSTORE_TEST_DSN',
    'trap cleanup EXIT', 'docker rm -fv "$id"', 'set -euo pipefail',
    '-timeout=10m', 'prerequisite-coverage.mjs postgres-measurements']) assert.ok(script.includes(token), token);
  assert.match(script, /postgres:18\.6-alpine@sha256:[a-f0-9]{64}/);
  assert.doesNotMatch(script, /\|\| true|pg_stat_statements_reset|docker (?:stop|rm).*\$\(docker/);
});

for (const mode of ['success', 'go-fail', 'cleanup-fail', 'go-fail-cleanup-fail', 'second-start-fail']) {
  test(`owned bootstrap preserves exits and only removes its own IDs: ${mode}`, () => {
    const dir = mkdtempSync(join(tmpdir(), 'engram-bootstrap-'));
    try {
      const events = passing('postgres-measurements').map(e => JSON.stringify(e)).join('\n') + '\n';
      writeFileSync(join(dir, 'events.json'), events);
      const stubs = {
        docker: `#!/bin/bash
printf '%s\\n' "$*" >> "$CALLS"
if [[ "$1" == run ]]; then
  if [[ "$*" == *engram1831* ]]; then
    [[ "$MODE" != second-start-fail ]] || exit 8
    printf '%064d\\n' 2
  else printf '%064d\\n' 1; fi
fi
if [[ "$1" == rm && "$MODE" == *cleanup-fail ]]; then exit 9; fi
`,
        go: `#!/bin/bash
printf '%s\\n' "$ENGRAM_DASHBOARD_BENCH_DSN" "$ENGRAM_PAUSE_BENCH_DSN" "\${CLOUDSTORE_TEST_DSN-unset}" > "$GO_ENV"
cat "$EVENTS"
[[ "$MODE" != go-fail* ]] || exit 7
`,
      };
      for (const [name, body] of Object.entries(stubs)) {
        writeFileSync(join(dir, name), body);
        chmodSync(join(dir, name), 0o700);
      }
      const result = spawnSync('/bin/bash', ['.github/scripts/postgres-measurements.sh'], {
        encoding: 'utf8', env: { ...process.env, PATH: dir + ':' + process.env.PATH,
          GITHUB_ACTIONS: 'true', GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1',
          RUNNER_TEMP: dir, CALLS: join(dir, 'calls'), GO_ENV: join(dir, 'go-env'),
          EVENTS: join(dir, 'events.json'), MODE: mode, CLOUDSTORE_TEST_DSN: 'must-not-inherit' },
      });
      const code = mode === 'second-start-fail' ? 8 : mode.startsWith('go-fail') ? 7 : mode === 'cleanup-fail' ? 1 : 0;
      assert.equal(result.status, code, result.stderr);
      assert.equal(result.stderr, '');
      const suffix = mode === 'success' || mode === 'cleanup-fail'
        ? 'Coverage evidence verified: postgres-measurements, 11 required cases passed.\n' : '';
      assert.equal(result.stdout, mode === 'second-start-fail' ? ''
        : events + `measurement_go_exit=${mode.startsWith('go-fail') ? 7 : 0} tee_exit=0\n` + suffix);
      const calls = readFileSync(join(dir, 'calls'), 'utf8').trim().split('\n');
      const ids = [1, 2].slice(0, mode === 'second-start-fail' ? 1 : 2).map(n => String(n).padStart(64, '0'));
      assert.deepEqual(calls.filter(s => s.startsWith('rm ')), ids.map(id => `rm -fv ${id}`));
      for (const role of ['engram1832', 'engram1831']) {
        const call = calls.find(s => s.startsWith('run ') && s.includes(role));
        assert.ok(call.includes(`--publish 127.0.0.1:${role === 'engram1832' ? 15432 : 15431}:5432`));
        assert.ok(call.includes('shared_preload_libraries=pg_stat_statements -c compute_query_id=on'));
      }
      if (mode !== 'second-start-fail') {
        assert.equal(readFileSync(join(dir, 'go-env'), 'utf8'),
          'postgres://engram1832@127.0.0.1:15432/engram1832_measurement?sslmode=disable\n' +
          'postgres://engram1831@127.0.0.1:15431/engram1831_measurement?sslmode=disable\nunset\n');
      }
    } finally { rmSync(dir, { recursive: true, force: true }); }
  });
}
