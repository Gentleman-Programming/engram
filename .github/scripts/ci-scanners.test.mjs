import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const root = process.cwd();
const runner = '.github/scripts/ci-scanner.sh';
const workflow = fs.readFileSync('.github/workflows/ci.yml', 'utf8').replaceAll('\r\n', '\n');
const job = (id) => workflow.match(new RegExp(`^  ${id}:\\n[\\s\\S]*?(?=^  [a-z][\\w-]*:|$(?![\\s\\S]))`, 'm'))?.[0];

for (const [id, name, version] of [
  ['govulncheck', 'Go Vulnerability Check', 'golang.org/x/vuln/cmd/govulncheck@v1.7.0'],
  ['actionlint', 'Workflow Validation', 'github.com/rhysd/actionlint/cmd/actionlint@v1.7.12'],
]) {
  test(`${name} is independent, read-only, pinned and does not mask failures`, () => {
    const body = job(id);
    assert.ok(body, `${id} job missing`);
    assert.match(body, new RegExp(`name: ${name}`));
    assert.match(body, /permissions:\n      contents: read\n/);
    assert.match(body, /persist-credentials: false/);
    assert.match(body, /timeout-minutes: 15/);
    assert.ok(body.includes(version));
    assert.match(body, /go-version: "1.26.10"/);
    assert.match(body, /GOTOOLCHAIN: local/);
    assert.match(body, /      - name: Install pinned (?:scanner|validator)\n        env:\n          GOBIN: \$\{\{ runner.temp \}\}\/ci-scanners/);
    assert.doesNotMatch(body.split('    steps:')[0], /runner\.temp/);
    assert.doesNotMatch(body, /continue-on-error|needs:|\n    if:|secrets\.|write|\|\| true/);
    for (const action of body.matchAll(/uses: (.+)/g)) {
      assert.match(action[1], /@[a-f0-9]{40} # v\d+/);
    }
    assert.match(body, new RegExp(`bash \\.github/scripts/ci-scanner\\.sh ${id} `));
  });
}

test('scanner inputs and existing CI triggers remain explicit', () => {
  assert.match(job('govulncheck') ?? '', /govulncheck" -format=text -scan=symbol \.\/\.\.\./);
  assert.match(job('govulncheck') ?? '', /GOOS: linux\n      GOARCH: amd64\n      CGO_ENABLED: "0"/);
  assert.match(job('actionlint') ?? '', /actionlint" -shellcheck= -pyflakes= \.github\/workflows\/\*\.yml \.github\/workflows\/\*\.yaml/);
  assert.match(workflow, /  pull_request:\n  merge_group:\n    types: \[checks_requested\]\n  push:\n    branches:\n      - main/);
  assert.match(workflow, /node --test \.github\/scripts\/ci-scanners.test.mjs/);
  for (const name of ['Unit Tests', 'E2E Tests', 'Plugin Tests']) assert.ok(workflow.includes(`name: ${name}`));
});

for (const [tool, code, category] of [
  ['govulncheck', 0, 'clean'], ['govulncheck', 3, 'findings'],
  ['govulncheck', 1, 'execution failed'], ['govulncheck', 2, 'execution failed'],
  ['govulncheck', 124, 'execution failed'],
  ['actionlint', 0, 'clean'], ['actionlint', 1, 'findings'],
  ['actionlint', 2, 'execution failed'], ['actionlint', 3, 'execution failed'],
]) {
  test(`${tool} exit ${code}: preserves streams and status, reports ${category}`, () => {
    const dir = fs.mkdtempSync(path.join(root, '.repro-scanner-'));
    try {
      const fake = path.join(dir, 'scanner');
      fs.writeFileSync(fake, `#!/usr/bin/env bash\nprintf 'scanner stdout\\n'\nprintf 'scanner stderr\\n' >&2\ntest "$1" = --fixture || exit 99\nexit ${code}\n`, { mode: 0o755 });
      const result = spawnSync('bash', [runner, tool, fake.replaceAll('\\', '/'), '--fixture'], { encoding: 'utf8' });
      assert.equal(result.error, undefined);
      assert.equal(result.status, code);
      assert.equal(result.stdout, 'scanner stdout\n');
      assert.equal(result.stderr, `scanner stderr\n${tool}: ${category} (exit ${code})\n`);
      assert.equal(fs.readFileSync(fake, 'utf8').includes(`exit ${code}`), true);
      assert.deepEqual(fs.readdirSync(dir), ['scanner']);
    } finally { fs.rmSync(dir, { recursive: true, force: true }); }
  });
}

test('invalid scanner selection is rejected before executing anything', () => {
  const result = spawnSync('bash', [runner, 'unknown', 'missing'], { encoding: 'utf8' });
  assert.equal(result.status, 2);
  assert.equal(result.stdout, '');
  assert.equal(result.stderr, 'Usage: bash .github/scripts/ci-scanner.sh {govulncheck|actionlint} EXECUTABLE [ARGS...]\n');
});

test('a missing scanner is an execution failure, never a clean scan', () => {
  const result = spawnSync('bash', [runner, 'govulncheck', './missing-scanner-2019'], { encoding: 'utf8' });
  assert.equal(result.status, 127);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /govulncheck: execution failed \(exit 127\)\n$/);
});
