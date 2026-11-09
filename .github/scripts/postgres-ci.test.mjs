import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const checker = '.github/scripts/postgres-ci-evidence.mjs';
const prefix = 'github.com/Gentleman-Programming/engram/v3/';
const selected = [
  ['internal/cloud/cloudstore', 'TestProjectSyncControlPersists'],
  ['internal/cloud/cloudserver', 'TestSessionAuthorityRegistrationPersistsOverHTTP'],
  ['cmd/engram', 'TestCloudRuntimeWiresManagedTokenAuthEndToEnd'],
];
const passing = () => selected.flatMap(([pkg, name]) => [
  { Action: 'pass', Package: prefix + pkg, Test: name },
  { Action: 'pass', Package: prefix + pkg },
]);
function check(events) {
  const dir = fs.mkdtempSync(path.join(process.cwd(), '.repro-postgres-'));
  try {
    const file = path.join(dir, 'events.json');
    fs.writeFileSync(file, events.map(e => typeof e === 'string' ? e : JSON.stringify(e)).join('\n') + '\n');
    return spawnSync(process.execPath, [checker, file], { encoding: 'utf8' });
  } finally { fs.rmSync(dir, { recursive: true, force: true }); }
}

test('requires successful database sentinel tests and packages, allowing unrelated skips', () => {
  const result = check([...passing(), { Action: 'skip', Package: prefix + selected[0][0], Test: 'TestDashboardReadModelPostgresCounts' }]);
  assert.equal(result.status, 0);
  assert.equal(result.stdout, 'PostgreSQL evidence verified: 3 packages and 3 database sentinel tests passed.\n');
  assert.equal(result.stderr, '');
});
for (const [name, events] of [
  ['missing database sentinel', passing().filter(e => e.Test !== selected[0][1])],
  ['missing package completion', passing().filter(e => e.Test || e.Package !== prefix + selected[1][0])],
  ['database skip', [...passing(), { Action: 'output', Output: 'CLOUDSTORE_TEST_DSN not set — skipping integration test\n' }, { Action: 'skip', Test: 'OtherDatabaseTest' }]],
  ['test failure', [...passing(), { Action: 'fail', Test: 'OtherDatabaseTest' }]],
  ['malformed evidence', [...passing(), 'not json']],
  ['empty evidence', []],
]) {
  test(`rejects ${name}`, () => {
    const result = check(events);
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /^PostgreSQL evidence rejected:/);
  });
}

test('PostgreSQL job is isolated and preserves existing CI jobs and triggers', () => {
  const workflow = fs.readFileSync('.github/workflows/ci.yml', 'utf8').replaceAll('\r\n', '\n');
  const body = workflow.match(/^  postgres-tests:\n[\s\S]*?(?=^  [a-z][\w-]*:|$(?![\s\S]))/m)?.[0];
  assert.ok(body, 'PostgreSQL job missing');
  for (const value of ['postgres:16-alpine', '--health-cmd "pg_isready', 'CLOUDSTORE_TEST_DSN:', 'go-version: "1.26.10"', 'persist-credentials: false', 'set -euo pipefail', 'go test -json -count=1 -timeout=10m ./internal/cloud/cloudstore ./internal/cloud/cloudserver ./cmd/engram', 'node .github/scripts/postgres-ci-evidence.mjs', 'SELECT version()', 'git rev-parse HEAD']) assert.ok(body.includes(value), value);
  assert.match(body, /permissions:\n      contents: read/);
  assert.match(body, /timeout-minutes: 20/);
  assert.doesNotMatch(body, /continue-on-error|needs:|\n    if:|secrets\.|-short|\|\| true/);
  for (const action of body.matchAll(/uses: (.+)/g)) assert.match(action[1], /@[a-f0-9]{40} # v\d+/);
  assert.match(workflow, /node --test \.github\/scripts\/postgres-ci.test.mjs/);
  assert.match(workflow, /  pull_request:\n  merge_group:\n    types: \[checks_requested\]\n  push:\n    branches:\n      - main/);
  for (const name of ['Unit Tests', 'E2E Tests', 'Plugin Tests']) assert.ok(workflow.includes(`name: ${name}`));
});
