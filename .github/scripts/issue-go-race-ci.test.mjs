import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const workflow = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const job = id => workflow.match(new RegExp(`^  ${id}:\\n[\\s\\S]*?(?=^  [a-z][\\w-]*:|$(?![\\s\\S]))`, 'm'))?.[0] ?? '';

test('full-suite race CI has a supported Linux toolchain and explicit compiler', () => {
  const body = job('race-tests');
  assert.match(body, /name: Go Race Tests/);
  assert.match(body, /runs-on: ubuntu-24.04/);
  assert.match(body, /CGO_ENABLED: "1"/);
  assert.match(body, /CC: gcc/);
  assert.match(body, /GOTOOLCHAIN: local/);
  assert.match(body, /go-version: "1.26.10"/);
  assert.match(body, /run: gcc --version/);
  assert.match(body, /run: \/usr\/bin\/time -p go test -race -timeout=20m \.\/\.\.\.\n/);
});

test('race job keeps failures visible, timing and an exact revision without scope exclusions', () => {
  const body = job('race-tests');
  assert.match(body, /timeout-minutes: 30/);
  assert.match(body, /ref: \$\{\{ github.sha \}\}/);
  assert.match(body, /run: git rev-parse HEAD/);
  assert.match(body, /permissions:\n      contents: read/);
  assert.match(body, /persist-credentials: false/);
  assert.doesNotMatch(body, /continue-on-error|needs:|\n    if:|\|\| true| -run | -short| -skip|secrets\.|contents: write/);
  for (const action of body.matchAll(/uses: (.+)/g)) assert.match(action[1], /@[a-f0-9]{40} # v\d+/);
});

test('existing suites, event coverage and policy-test discovery remain intact', () => {
  assert.match(job('unit-tests'), /run: go test \.\/\.\.\.\n/);
  assert.doesNotMatch(job('unit-tests'), /-timeout=20m/);
  assert.match(job('e2e-tests'), /run: go test -tags e2e \.\/internal\/server\/\.\.\./);
  assert.match(job('plugin-tests'), /run: npm test/);
  assert.match(workflow, /on:\n  pull_request:\n  merge_group:\n    types: \[checks_requested\]\n  push:\n    branches:\n      - main/);
  assert.match(job('policy-helper-tests'), /node --test \.github\/scripts\/issue-\*\.test.mjs/);
});
