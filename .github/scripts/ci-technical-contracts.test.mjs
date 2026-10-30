import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const job = (text, id) => text.match(new RegExp(`^  ${id}:\\n[\\s\\S]*?(?=^  [a-z][\\w-]*:|$(?![\\s\\S]))`, 'm'))?.[0] ?? '';
const withoutHold = text => text.replace(/      - name: Await selected CI proof\n[\s\S]*?(?=      - name: Checkout\n)/, '');

function discovery(text) {
  const body = job(text, 'policy-helper-tests');
  assert.match(body, /run: node --test \.github\/scripts\/ci-technical-contracts\.test\.mjs\n/);
  assert.match(body, /run: node --test \.github\/scripts\/issue-\*\.test.mjs\n/);
}

test('independent technical discovery in the existing Policy Helper Tests job', () => discovery(source));

function race(text) {
  const body = job(text, 'race-tests');
  for (const line of ['name: Go Race Tests', 'runs-on: ubuntu-24.04', 'timeout-minutes: 30',
    'CGO_ENABLED: "1"', 'CC: gcc', 'GOTOOLCHAIN: local', 'go-version: "1.26.10"',
    'ref: ${{ github.sha }}', 'persist-credentials: false', 'run: git rev-parse HEAD',
    'run: gcc --version', 'run: /usr/bin/time -p go test -race -timeout=20m ./...']) {
    assert.ok(body.split('\n').some(value => value.trim() === line), line);
  }
  assert.match(body, /permissions:\n      contents: read\n    env:/);
  // Only the cache writer may be conditional; checkout, preparation and tests never are.
  const save = raceSteps(body).filter(step => /uses: actions\/cache\/save@/.test(step));
  if (save.length) raceWriter(text);
  const guard = save.length === 1 ? save[0].match(/^        if: \$\{\{ .+ \}\}\n/m)?.[0] : undefined;
  const unconditional = guard ? body.replace(save[0], () => save[0].replace(guard, '')) : body;
  assert.doesNotMatch(unconditional, /continue-on-error|needs:|\n\s+if:|\|\| true| -run | -short| -skip|secrets\.|: write|environment:/);
  for (const action of body.matchAll(/uses: (.+)/g)) assert.match(action[1], /@[a-f0-9]{40} # v\d+$/);
}

// This is deliberately the Race step layout, not a general YAML parser.
const raceSteps = body => body.match(/^      - name: .*\n[\s\S]*?(?=^      - name: |$(?![\s\S]))/gm) ?? [];
const cachePin = '55cc8345863c7cc4c66a329aec7e433d2d1c52a9';
function cacheSteps(text) {
  const steps = raceSteps(job(text, 'race-tests'));
  const select = action => {
    const found = steps.filter(step => step.includes(`uses: actions/${action}@`));
    assert.equal(found.length, 1, `single ${action} owner`);
    return found[0];
  };
  return { steps, restore: select('cache/restore'), save: select('cache/save') };
}
function raceOwner(text) {
  const body = job(text, 'race-tests');
  const { steps, restore, save } = cacheSteps(text);
  assert.equal((body.match(/uses: actions\/cache\//g) ?? []).length, 2);
  assert.doesNotMatch(body, /uses: actions\/cache@|enableCrossOsArchive: true|cache-mode:|(?:lookup-only|fail-on-cache-miss): true/);
  assert.match(steps.find(s => s.includes('uses: actions/setup-go@')) ?? '', /go-version: "1\.26\.10"\n          cache: false\n/);
  for (const step of [restore, save]) {
    assert.match(step, /path: \|\n            \$\{\{ steps\.race-cache\.outputs\.gomodcache \}\}\n            \$\{\{ steps\.race-cache\.outputs\.gocache \}\}/);
    assert.match(step, /key: \$\{\{ steps\.race-cache\.outputs\.key \}\}/);
    assert.doesNotMatch(step, /cache-primary-key|retry|continue-on-error/);
  }
  assert.match(restore, /id: race-cache-restore\n/);
  assert.match(restore, /restore-keys: \$\{\{ steps\.race-cache\.outputs\.prefix \}\}\n/);
  assert.doesNotMatch(save, /restore-keys:/);
  const prepare = steps.find(s => s.includes('id: race-cache\n'));
  assert.ok(prepare, 'precomputed cache inputs');
  assert.ok(steps.indexOf(prepare) < steps.indexOf(restore));
  assert.ok(body.indexOf(restore) < body.indexOf('run: /usr/bin/time -p go test -race'));
  assert.ok(body.indexOf(save) > body.indexOf('run: /usr/bin/time -p go test -race'));
}
function raceIdentity(text) {
  const { steps, restore, save } = cacheSteps(text);
  for (const [step, action] of [[restore, 'restore'], [save, 'save']]) {
    assert.ok(step.includes(`uses: actions/cache/${action}@${cachePin} # v6\n`), 'verified immutable v6.1.0 pin');
  }
  const prepare = steps.find(s => s.includes('id: race-cache\n')) ?? '';
  // Identity must bind actual toolchain/compiler inputs, not just the label "gcc".
  for (const input of ['runner.os', 'runner.arch', 'ImageOS', 'ImageVersion',
    'go env GOVERSION', 'go env GOOS', 'go env GOARCH', 'go env CGO_ENABLED',
    "hashFiles('go.mod', 'go.sum')", 'command -v', 'readlink -f', 'sha256sum',
    '-dumpfullversion', '-dumpmachine', '--version', 'go env GOMODCACHE', 'go env GOCACHE',
    'GITHUB_OUTPUT', 'github.sha', 'race']) assert.ok(prepare.includes(input), `cache identity: ${input}`);
  assert.match(prepare, /set -euo pipefail/);
  assert.match(prepare, /\[\[.*\/\*.*\]\]/, 'absolute cache paths validated');
  assert.match(prepare, /\[\[.*[\\]r.*[\\]n.*\]\]/, 'scalar outputs reject record injection');
  for (const field of ['gomodcache', 'gocache', 'prefix', 'key']) {
    assert.ok(prepare.includes(`${field}=%s\\n`), `fixed output record ${field}`);
  }
  assert.match(prepare, /prefix=.*(?:compat|identity)/, 'complete compatibility prefix');
  assert.match(prepare, /key=.*\$\{?prefix\}?.*\$\{?[^\n]*(?:sha|revision)/, 'primary extends same prefix with tested revision');
  assert.equal((restore.match(/restore-keys:/g) ?? []).length, 1);
  assert.doesNotMatch(restore, /restore-keys: \|/, 'no additional broad fallback');
}
function raceWriter(text) {
  const { save } = cacheSteps(text);
  const expression = save.match(/^        if: \$\{\{ (.+) \}\}$/m)?.[1];
  // Evaluate the declared predicate, constrained to the approved fixed expression.
  // No disconnected duplicate implementation or arbitrary workflow evaluation.
  const expected = "success() && github.repository == 'Gentleman-Programming/engram' && github.event_name == 'push' && github.ref == 'refs/heads/main' && steps.race-cache-restore.outputs.cache-hit != 'true'";
  assert.equal(expression, expected, 'only trusted successful main misses may save');
  const executable = expression.replaceAll('steps.race-cache-restore.outputs.cache-hit', 'hit');
  const evaluate = new Function('success', 'github', 'hit', `return (${executable});`);
  for (const event_name of ['pull_request', 'merge_group', 'push']) {
    for (const repository of ['Gentleman-Programming/engram', 'fork/engram']) {
      for (const ref of ['refs/heads/main', 'refs/heads/feature', 'refs/pull/1/merge']) {
        for (const outcome of ['success', 'failure', 'cancelled']) {
          for (const hit of ['true', 'false', '', undefined]) {
            const allowed = event_name === 'push' && repository === 'Gentleman-Programming/engram'
              && ref === 'refs/heads/main' && outcome === 'success' && hit !== 'true';
            assert.equal(evaluate(() => outcome === 'success', { event_name, repository, ref }, hit), allowed,
              JSON.stringify({ event_name, repository, ref, outcome, hit }));
          }
        }
      }
    }
  }
}
for (const [name, check] of Object.entries({ raceOwner, raceIdentity, raceWriter })) {
  test(`${name} explicit Race cache contract (#2192)`, () => check(source));
}
test('Race checkout, prerequisites and full suite remain unconditional with cache (#2192)', () => {
  raceOwner(source);
  race(source);
  const original = job(source, 'race-tests');
  for (const step of raceSteps(original).filter(s => !s.includes('uses: actions/cache/save@'))) {
    const changed = step.replace(/^(      - name: .*\n)/, '$1        if: false\n');
    assert.throws(() => race(source.replace(original, () => original.replace(step, () => changed))), undefined, step.split('\n')[0]);
  }
});
test('Race save guard cannot hide forbidden step policies (#2192)', () => {
  race(source);
  const original = job(source, 'race-tests');
  const { save } = cacheSteps(source);
  for (const inserted of ['env: ${{ secrets.ESCAPE }}', 'permissions: write',
    'environment: escape', 'if: false', 'continue-on-error: true']) {
    const changed = save.replace(/^(      - name: .*\n)/, `$1        ${inserted}\n`);
    assert.throws(() => race(source.replace(original, () => original.replace(save, () => changed))), undefined, inserted);
  }
});
test('Race cache rejects isolated owner, identity, fallback and writer regressions (#2192)', () => {
  raceOwner(source); raceIdentity(source); raceWriter(source);
  const original = job(source, 'race-tests');
  const { restore } = cacheSteps(source);
  for (const option of ['lookup-only', 'fail-on-cache-miss']) {
    const withOption = value => restore.replace('        with:\n', () => `        with:\n          ${option}: ${value}\n`);
    const changed = source.replace(original, () => original.replace(restore, () => withOption('false')));
    // Bash ANSI-C quoting ($') and positional parameters are literal source, not JS replacement tokens.
    assert.equal(changed.replace(withOption('false'), () => restore), source, 'only the intended restore option changes');
    assert.equal(job(changed, 'race-tests').replace(withOption('false'), () => restore), original);
    raceOwner(changed);
    assert.throws(() => raceOwner(source.replace(original, () => original.replace(restore, () => withOption('true')))), undefined, option);
  }
  for (const [before, after, check] of [
    ['cache: false', 'cache: true', raceOwner],
    [cachePin, 'v6', raceIdentity],
    ["hashFiles('go.mod', 'go.sum')", "hashFiles('go.mod')", raceIdentity],
    ['-dumpmachine', '-dumpversion', raceIdentity],
    ['ImageVersion', 'IgnoredImage', raceIdentity],
    ['ImageOS', 'IgnoredOS', raceIdentity],
    ['runner.arch', 'runner.name', raceIdentity],
    ['go env GOVERSION', 'go version', raceIdentity],
    ['go env CGO_ENABLED', 'echo 1', raceIdentity],
    ['github.sha', 'github.ref', raceIdentity],
    ['readlink -f', 'echo', raceIdentity],
    ['sha256sum', 'echo', raceIdentity],
    ['-dumpfullversion', '-dumpversion', raceIdentity],
    ['outputs.gocache', 'outputs.gomodcache', raceOwner],
    ['outputs.prefix', 'outputs.legacy', raceOwner],
    ['outputs.key', 'outputs.cache-primary-key', raceOwner],
    ["success() &&", '', raceWriter],
    ["github.repository == 'Gentleman-Programming/engram' && ", '', raceWriter],
    ["github.event_name == 'push' && ", '', raceWriter],
    ["github.ref == 'refs/heads/main' && ", '', raceWriter],
    [" != 'true'", " == 'true'", raceWriter],
  ]) {
    const target = check === raceIdentity && before !== cachePin
      ? raceSteps(original).find(step => step.includes('id: race-cache\n')) : original;
    assert.ok(target?.includes(before), `Race mutation target: ${before}`);
    const changed = original.replace(target, () => target.replace(before, () => after));
    assert.throws(() => check(source.replace(original, () => changed)), undefined, before);
  }
});

function verification(text) {
  assert.match(text, /on:\n  pull_request:\n  merge_group:\n    types: \[checks_requested\]\n  push:\n    branches:\n      - main\n/);
  assert.match(text, /^permissions:\n  contents: read\n\n/m);
  const unit = job(withoutHold(text), 'unit-tests');
  assert.match(unit, /name: Unit Tests\n/);
  assert.doesNotMatch(unit, /continue-on-error|environment:|\n\s+if:|\|\| true|: write|secrets\.|-timeout=20m/);
  const ordered = ['name: Set up Go', 'go-version: "1.26.10"',
    'name: Verify module metadata is tidy\n        run: go mod tidy -diff\n',
    'name: Verify generated templ output\n', 'make templ\n',
    "git diff --exit-code -- 'internal/cloud/dashboard/*_templ.go'\n",
    "untracked_templ_outputs=$(git ls-files --others --exclude-standard -- 'internal/cloud/dashboard/*_templ.go')\n",
    'if [[ -n "${untracked_templ_outputs}" ]]; then\n', 'exit 1\n',
    'run: make deadcode-check\n', 'run: go test ./...\n',
    'run: go build -o engram-selftest ./cmd/engram && ./engram-selftest test --quick\n'];
  let previous = -1;
  for (const command of ordered) {
    const position = unit.indexOf(command);
    assert.ok(position > previous, `missing or reordered: ${command}`);
    previous = position;
  }
  assert.match(job(text, 'e2e-tests'), /name: E2E Tests\n/);
  assert.match(job(text, 'e2e-tests'), /run: go test -tags e2e \.\/internal\/server\/\.\.\.\n/);
  assert.match(job(text, 'plugin-tests'), /name: Plugin Tests\n/);
  assert.match(job(text, 'plugin-tests'), /run: npm test\n/);
}

// Only evaluate the existing fixed Actions expression subset with no proof selection.
// Proof-specific predicate spelling and nonempty selector semantics belong to issue-ci-concurrency.
function routing(text, overrides = {}) {
  const block = text.match(/^concurrency:\n([\s\S]*?)(?=^\S)/m)?.[1];
  assert.ok(block);
  const group = block.match(/^  group: (.+)$/m)?.[1];
  const cancel = block.match(/^  cancel-in-progress: \$\{\{ (.+) \}\}$/m)?.[1];
  assert.ok(group); assert.ok(cancel);
  const github = { workflow: 'CI', event_name: 'pull_request', head_ref: 'feature/shared',
    event: { pull_request: { number: 42 } }, run_id: 100, run_attempt: 1, ...overrides };
  const startsWith = (value, prefix) => value.toLowerCase().startsWith(prefix.toLowerCase());
  const format = (template, ...args) => template.replace(/\{(\d+)\}/g, (_, n) => String(args[n]));
  const evaluate = expression => new Function('github', 'vars', 'startsWith', 'format',
    `return (${expression});`)(github, { ISSUE_CI_PROOF_CI: '' }, startsWith, format);
  return { group: group.replace(/\$\{\{ (.*?) \}\}/g, (_, expression) => evaluate(expression)), cancel: evaluate(cancel) };
}

function concurrency(text) {
  const ordinary = routing(text);
  assert.deepEqual(ordinary, { group: 'CI-pr-42', cancel: true });
  assert.deepEqual(routing(text, { run_id: 101 }), ordinary);
  assert.deepEqual(routing(text, { run_attempt: 2 }), ordinary);
  assert.deepEqual(routing(text, { event: { pull_request: { number: 43 } } }), { group: 'CI-pr-43', cancel: true });
  assert.deepEqual(routing(text, { workflow: 'Other CI' }), { group: 'Other CI-pr-42', cancel: true });
  for (const event_name of ['merge_group', 'push']) {
    const context = { event_name, head_ref: '', event: {} };
    for (const [extra, group] of [[{}, 'CI-run-100-1'], [{ run_id: 101 }, 'CI-run-101-1'],
      [{ run_attempt: 2 }, 'CI-run-100-2'], [{ workflow: 'Other CI' }, 'Other CI-run-100-1']]) {
      assert.deepEqual(routing(text, { ...context, ...extra }), { group, cancel: false });
    }
  }
  assert.notEqual(routing(text, { event_name: 'merge_group', run_id: 102 }).group,
    routing(text, { event_name: 'push', run_id: 103 }).group);
}

for (const [name, check] of Object.entries({ race, verification, concurrency })) {
  test(`${name} technical contract without proof qualification`, () => {
    check(source);
    check(withoutHold(source));
  });
}

const mutations = {
  race: [
    ['ubuntu-24.04', 'ubuntu-latest'], ['go-version: "1.26.10"', 'go-version: "1.26.9"'],
    ['CGO_ENABLED: "1"', 'CGO_ENABLED: "0"'], ['CC: gcc', 'CC: clang'],
    ['GOTOOLCHAIN: local', 'GOTOOLCHAIN: auto'], ['run: gcc --version', 'run: true'],
    ['timeout-minutes: 30', 'timeout-minutes: 20'], ['-timeout=20m', '-timeout=10m'],
    ['go test -race', 'go test'], ['-timeout=20m ./...', '-timeout=20m ./internal/store/...'],
    ['-timeout=20m ./...', '-timeout=20m -run TestOne ./...'],
    ['-timeout=20m ./...', '-timeout=20m ./... || true'],
    ['persist-credentials: false', 'persist-credentials: true'],
    ['ref: ${{ github.sha }}', 'ref: main'], ['run: git rev-parse HEAD', 'run: true'],
    ['contents: read\n    env:', 'contents: write\n    env:'],
    ['    name: Go Race Tests', '    if: false\n    name: Go Race Tests'],
    ['    name: Go Race Tests', '    needs: unit-tests\n    name: Go Race Tests'],
    ['    name: Go Race Tests', '    continue-on-error: true\n    name: Go Race Tests'],
    ['    name: Go Race Tests', '    environment: ${{ secrets.ESCAPE }}\n    name: Go Race Tests'],
    ['actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6', 'actions/checkout@v6'],
  ],
  verification: [
    ['run: go mod tidy -diff', 'run: go mod tidy'], ['make templ\n', 'true\n'],
    ['make deadcode-check\n', 'true\n'], ['run: go test ./...\n', 'run: go test -short ./...\n'],
    ['go build -o engram-selftest ./cmd/engram &&', 'go build -o engram-selftest ./cmd/engram ;'],
    ['./engram-selftest test --quick', 'true'], ['exit 1\n', 'exit 0\n'],
    ['run: make deadcode-check', 'run: make deadcode-check || true'],
    ['    name: Unit Tests', '    if: false\n    name: Unit Tests'],
    ['    name: Unit Tests', '    continue-on-error: true\n    name: Unit Tests'],
    ['    name: Unit Tests', '    environment: escape\n    name: Unit Tests'],
    ['permissions:\n  contents: read', 'permissions:\n  contents: write'],
    ['      issues: read', '      issues: write'], ['      - main\n', '      - feature\n'],
    ['run: go test -tags e2e ./internal/server/...', 'run: true'], ['working-directory: plugin/pi\n        run: npm test', 'working-directory: plugin/pi\n        run: true'],
  ],
  concurrency: [
    ["format('pr-{0}', github.event.pull_request.number)", "format('pr-{0}', github.head_ref)"],
    ['${{ github.workflow }}-', 'shared-'],
    ["format('run-{0}-{1}', github.run_id, github.run_attempt)", "format('run-{0}', github.run_id)"],
    ["format('run-{0}-{1}', github.run_id, github.run_attempt)", "'shared-protected'"],
    ['cancel-in-progress: ${{', 'cancel-in-progress: ${{ true ||'],
    ['cancel-in-progress: ${{', 'cancel-in-progress: ${{ false &&'],
  ],
};
for (const [category, cases] of Object.entries(mutations)) {
  const check = { race, verification, concurrency }[category];
  test(`${category} rejects isolated workflow regressions`, () => {
    for (const [before, after] of cases) {
      const target = category === 'race' ? job(source, 'race-tests') : source;
      assert.ok(target.includes(before), `fixture target: ${before}`);
      const changed = target.replace(before, () => after);
      assert.throws(() => check(category === 'race' ? source.replace(target, () => changed) : changed), undefined, `${before} -> ${after}`);
    }
  });
}

test('verification rejects missing setup and reordered preflight/build stages', () => {
  const setup = '      - name: Set up Go\n        uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6\n        with:\n          go-version: "1.26.10"\n';
  const unit = job(source, 'unit-tests');
  assert.ok(unit.includes(setup));
  assert.throws(() => verification(source.replace(unit, () => unit.replace(setup, ''))));
  for (const [first, second] of [['go mod tidy -diff', 'make templ'], ['make templ', 'make deadcode-check'],
    ['make deadcode-check', 'go test ./...'], ['go test ./...', 'go build -o engram-selftest ./cmd/engram && ./engram-selftest test --quick']]) {
    const changed = unit.replace(first, '__SWAP__').replace(second, () => first).replace('__SWAP__', () => second);
    assert.throws(() => verification(source.replace(unit, () => changed)), undefined, `${first} before ${second}`);
  }
});

function macos(text) {
  const body = job(text, 'macos-native-tests');
  assert.ok(body, 'native macOS job must exist');
  for (const value of ['name: Native macOS ARM64 Tests', 'runs-on: macos-15', 'timeout-minutes: 20',
    'GOTOOLCHAIN: local', 'go-version: "1.26.10"', 'node-version: "24"',
    'ref: ${{ github.sha }}', 'persist-credentials: false',
    'test "$(uname -sm)" = "Darwin arm64"', 'test "$(go env GOVERSION)" = go1.26.10',
    'test "$(go env GOHOSTOS)/$(go env GOHOSTARCH)" = darwin/arm64',
    'test "$(go env GOOS)/$(go env GOARCH)" = darwin/arm64',
    'test "$(node -p \'process.platform + "/" + process.arch\')" = darwin/arm64',
    '/usr/bin/jq --version', '/bin/bash --version',
    'set -euo pipefail', 'go test -json -count=1 -timeout=10m ./...',
    'tee "$RUNNER_TEMP/macos-native-tests.json"', 'if: ${{ !cancelled() }}',
    'HOME=$state/home', 'TMPDIR=$state/tmp', 'ENGRAM_DATA_DIR=$state/store',
    'process.env.GITHUB_STEP_SUMMARY']) assert.ok(body.includes(value), value);
  assert.match(body, /permissions:\n      contents: read\n/);
  assert.equal(body.split('set -euo pipefail').length - 1, 3, 'all three shell stages preserve failures');
  assert.doesNotMatch(body, /continue-on-error|needs:|services:|: write|secrets\.| -short| -skip| -run | -race|\|\| true|npm ci|sudo |ln -s.*\/usr\/bin/);
  for (const action of body.matchAll(/uses: (.+)/g)) assert.match(action[1], /@[a-f0-9]{40} # v\d+$/);
  const positions = ['name: Set up Go', 'name: Set up Node.js', 'name: Verify native prerequisites',
    'name: Isolate test state', 'name: Run native Go suite', 'name: Summarize native scope'].map(x => body.indexOf(x));
  assert.ok(positions.every((p, i) => p >= 0 && (!i || p > positions[i - 1])));
}

test('native macOS foundation contract', () => macos(source));
test('native macOS rejects scope, prerequisites, budget and failure regressions', () => {
  const original = job(source, 'macos-native-tests');
  for (const [before, after] of [
    ['runs-on: macos-15', 'runs-on: macos-latest'], ['timeout-minutes: 20', 'timeout-minutes: 30'],
    ['go-version: "1.26.10"', 'go-version: "1.26.9"'], ['node-version: "24"', 'node-version: "22"'],
    ['/usr/bin/jq --version', 'jq --version'], ['Darwin arm64', 'Darwin x86_64'],
    ['-timeout=10m', '-timeout=20m'], ['-count=1', '-count=2'],
    ['set -euo pipefail', 'set -eu'], ['HOME=$state/home', 'HOME=/Users/runner'],
    ['if: ${{ !cancelled() }}', 'if: success()'], ['persist-credentials: false', 'persist-credentials: true'],
  ]) {
    assert.ok(original.includes(before), before);
    assert.throws(() => macos(source.replace(original, () => original.replace(before, () => after))), undefined, before);
  }
  assert.throws(() => macos(source.replace(original, () => original.replace('    steps:', '    continue-on-error: true\n    steps:'))));
});

import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';

function summarize(events, raw, outcome = 'success') {
  const match = job(source, 'macos-native-tests').match(/node <<'NODE'\n([\s\S]*?)\n          NODE/);
  assert.ok(match, 'workflow summary script');
  const dir = mkdtempSync(join(tmpdir(), 'macos-summary-'));
  try {
    if (events !== null) writeFileSync(join(dir, 'macos-native-tests.json'), raw ?? events.map(e => JSON.stringify(e)).join('\n'));
    const env = { ...process.env, NATIVE_TEST_OUTCOME: outcome, RUNNER_TEMP: dir, GITHUB_STEP_SUMMARY: join(dir, 'summary') };
    if (outcome === null) delete env.NATIVE_TEST_OUTCOME;
    const result = spawnSync(process.execPath, ['-'], { input: match[1].replace(/^          /gm, ''),
      encoding: 'utf8', env });
    const markdown = readFileSync(join(dir, 'summary'), 'utf8');
    if (events !== null) assert.equal(readFileSync(join(dir, 'macos-native-tests.json'), 'utf8'), raw ?? events.map(e => JSON.stringify(e)).join('\n'));
    return { status: result.status, stdout: result.stdout, stderr: result.stderr, markdown };
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

function adapters(text) {
  const piScript = JSON.parse(readFileSync(new URL('../../plugin/pi/package.json', import.meta.url), 'utf8')).scripts.test;
  const body = job(text, 'macos-adapter-tests');
  assert.ok(body, 'independent native adapter job');
  for (const value of ['runs-on: macos-15', 'timeout-minutes: 15', 'GOTOOLCHAIN: local',
    'go-version-file: go.mod', 'node-version: "24"', 'ref: ${{ github.sha }}', 'persist-credentials: false',
    'test "$(uname -sm)" = "Darwin arm64"', 'test "$(go env GOVERSION)" = go1.26.10',
    'test "$(go env GOOS)/$(go env GOARCH)" = darwin/arm64',
    'test "$(go env GOHOSTOS)/$(go env GOHOSTARCH)" = darwin/arm64',
    'test "$(node -p \'process.platform + "/" + process.arch\')" = darwin/arm64',
    'test "$(node -p \'process.versions.node.split(".")[0]\')" = 24',
    'state=$(cd "$state" && pwd -P)', 'HOME=$state/home', 'TMPDIR=$state/tmp',
    'GOMODCACHE=$(go env GOMODCACHE)', 'GOCACHE=$(go env GOCACHE)',
    'node_modules/typebox/package.json', 'docs/contracts/agent-contract-v1.json',
    'npm ci --ignore-scripts --legacy-peer-deps', 'go mod download',
    'go build -mod=readonly -o "$RUNNER_TEMP/pi-test-server" ./plugin/pi/test/support/real-server',
    'ENGRAM_PI_TEST_SERVER_BINARY=$RUNNER_TEMP/pi-test-server',
    piScript.replace('node --test ', 'node --test --test-reporter=tap ') + ' | tee "$RUNNER_TEMP/pi.tap"',
    'node --test --test-reporter=tap plugin/opencode/engram.test.mjs plugin/opencode/engram.v2.test.mjs plugin/opencode/engram.v2.real-server.test.mjs | tee "$RUNNER_TEMP/opencode.tap"',
    'PI_OUTCOME: ${{ steps.pi.outcome }}', 'OPENCODE_OUTCOME: ${{ steps.opencode.outcome }}',
    'if: ${{ !cancelled() }}']) assert.ok(body.includes(value), value);
  assert.match(body, /permissions:\n      contents: read\n/);
  assert.match(body, /name: Build existing Pi fixture\n        timeout-minutes: 2/);
  assert.match(body, /name: Run Pi adapters\n        id: pi\n        timeout-minutes: 5\n        working-directory: plugin\/pi/);
  assert.match(body, /name: Run OpenCode adapters\n        id: opencode\n        timeout-minutes: 5/);
  assert.equal((body.match(/set -euo pipefail/g) ?? []).length, 5);
  assert.doesNotMatch(body, /continue-on-error|needs:|services:|: write|secrets\.|sudo |\|\| true| -skip| -short|ENGRAM_PI_LIFECYCLE_CLI=|export.*NODE_OPTIONS=/);
  for (const action of body.matchAll(/uses: (.+)/g)) assert.match(action[1], /@[a-f0-9]{40} # v\d+$/);
}

test('native adapters retain source commands, native prerequisites, isolation and budgets', () => adapters(source));
test('native adapter contract rejects weakened placement, prerequisites and exits', () => {
  const original = job(source, 'macos-adapter-tests');
  for (const before of ['runs-on: macos-15', 'timeout-minutes: 15', 'timeout-minutes: 2', 'timeout-minutes: 5',
    'go-version-file: go.mod', 'node-version: "24"', 'persist-credentials: false', 'set -euo pipefail',
    'Darwin arm64', 'go1.26.10', 'darwin/arm64', 'state=$(cd "$state" && pwd -P)',
    'node_modules/typebox/package.json', 'docs/contracts/agent-contract-v1.json',
    'npm ci --ignore-scripts --legacy-peer-deps', 'ENGRAM_PI_TEST_SERVER_BINARY=$RUNNER_TEMP/pi-test-server',
    '--test-concurrency=1', 'plugin/opencode/engram.v2.real-server.test.mjs', 'if: ${{ !cancelled() }}']) {
    assert.ok(original.includes(before), before);
    assert.throws(() => adapters(source.replace(original, () => original.replace(before, '__REMOVED__'))), undefined, before);
  }
  for (const inserted of ['continue-on-error: true', 'needs: plugin-tests', 'permissions:\n      contents: write']) {
    assert.throws(() => adapters(source.replace(original, () => original.replace('    steps:', () => '    ' + inserted + '\n    steps:'))));
  }
});


for (const suite of ['Pi', 'OpenCode']) {
  test(`native ${suite} reporter stays out of child fixtures and preserves failures`, () => {
    const script = job(source, 'macos-adapter-tests').match(new RegExp(
      `      - name: Run ${suite} adapters\\n[\\s\\S]*?        run: \\|\\n([\\s\\S]*?)(?=      - name:)`))[1].replace(/^          /gm, '');
    const dir = mkdtempSync(join(tmpdir(), 'adapter-reporter-'));
    try {
      writeFileSync(join(dir, 'package.json'), readFileSync(new URL('../../plugin/pi/package.json', import.meta.url)));
      const files = suite === 'Pi' ? ['test/probe.test.mjs'] : [
        'plugin/opencode/engram.test.mjs', 'plugin/opencode/engram.v2.test.mjs', 'plugin/opencode/engram.v2.real-server.test.mjs'];
      writeFileSync(join(dir, 'child.mjs'), "import { test } from 'node:test'; test('leaf', () => { if (process.env.ADAPTER_PROBE_FAIL) throw Error('fixture sentinel'); });");
      for (const file of files) {
        mkdirSync(join(dir, file, '..'), { recursive: true });
        writeFileSync(join(dir, file), `import assert from 'node:assert/strict'; import { test } from 'node:test'; import { spawnSync } from 'node:child_process';
          test('child reporter', () => { const env = { ...process.env }; delete env.NODE_TEST_CONTEXT;
            const r = spawnSync(process.execPath, ['--test', '--test-reporter=tap', 'child.mjs'], { env, encoding: 'utf8' });
            assert.equal(r.stderr, ''); assert.equal(r.status, 0, r.stdout); assert.equal(process.env.NODE_OPTIONS, undefined); });`);
      }
      const env = { ...process.env, RUNNER_TEMP: dir.replaceAll('\\', '/'), HOME: dir, NODE_OPTIONS: '--max-old-space-size=128' };
      delete env.NODE_TEST_CONTEXT;
      for (const fail of [false, true]) {
        const result = spawnSync('bash', ['-c', script], { cwd: dir, encoding: 'utf8',
          env: { ...env, ...(fail ? { ADAPTER_PROBE_FAIL: '1' } : {}) } });
        assert.equal(result.status, fail ? 1 : 0, result.stdout + result.stderr);
        assert.equal(result.stderr, '');
        assert.match(result.stdout, /^TAP version 13/m);
        assert.match(result.stdout, new RegExp(`^# ${fail ? 'fail' : 'pass'} ${files.length}$`, 'm'));
        assert.doesNotMatch(result.stdout, /ERR_INVALID_ARG_VALUE/);
        if (fail) assert.match(result.stdout, /fixture sentinel/);
        assert.equal(readFileSync(join(dir, suite.toLowerCase() + '.tap'), 'utf8'), result.stdout);
      }
    } finally { rmSync(dir, { recursive: true, force: true }); }
  });
}

const piRequired = ['Pi native saves persist under separate host sessions and stop on failed registration',
  'existing CLI verifies real Pi registration without writing',
  'packaged real workflows run all eight tests with the prebuilt fixture binary'];
const ocRequired = ['V2 inbox prompts stay idempotent and deleted across real server restarts'];
const host = 'actual Pi hosts retain attribution through compaction, resume and refusal';
const hostReason = 'opt-in: set ENGRAM_PI_LIFECYCLE_CLI to the installed Pi CLI entry point';
function tap(names, skipped = []) {
  const all = [...names.map(name => [name, '']), ...skipped];
  return 'TAP version 13\n' + all.map(([name, reason], i) => `ok ${i + 1} - ${name}${reason ? ' # SKIP ' + reason : ''}\n`).join('')
    + `1..${all.length}\n# tests ${all.length}\n# suites 0\n# pass ${names.length}\n# fail 0\n# cancelled 0\n# skipped ${skipped.length}\n# todo 0\n# duration_ms 1\n`;
}
function adapterSummary(pi = tap(piRequired, [[host, hostReason]]), oc = tap(ocRequired), outcome = 'success') {
  const match = job(source, 'macos-adapter-tests').match(/node <<'NODE'\n([\s\S]*?)\n          NODE/);
  assert.ok(match, 'inline adapter summary');
  const dir = mkdtempSync(join(tmpdir(), 'adapter-summary-'));
  try {
    if (pi !== null) writeFileSync(join(dir, 'pi.tap'), pi);
    writeFileSync(join(dir, 'opencode.tap'), oc);
    const result = spawnSync(process.execPath, ['-'], { input: match[1].replace(/^          /gm, ''), encoding: 'utf8',
      env: { ...process.env, RUNNER_TEMP: dir, GITHUB_STEP_SUMMARY: join(dir, 'summary'), PI_OUTCOME: outcome, OPENCODE_OUTCOME: outcome } });
    const report = JSON.parse(result.stdout);
    assert.equal(result.stderr, '');
    assert.equal(readFileSync(join(dir, 'summary'), 'utf8'), '## Native macOS adapters\n\n```json\n' + result.stdout.trim() + '\n```\n');
    return { ...result, report };
  } finally { rmSync(dir, { recursive: true, force: true }); }
}
test('adapter evidence preserves counts, repeated parameterized names and installed-host exclusion', () => {
  const repeated = 'V2 degraded warning preserves completed content and pending unknown results';
  const result = adapterSummary(undefined, tap([...ocRequired, repeated, repeated]));
  assert.equal(result.status, 0);
  assert.equal(result.report.coverage, 'complete-with-listed-exclusions');
  assert.deepEqual(result.report.suites.pi.skips, [{ name: host, reason: hostReason }]);
  assert.deepEqual(result.report.suites.pi.counts, { tests: 4, suites: 0, pass: 3, fail: 0, cancelled: 0, skipped: 1, todo: 0 });
  assert.deepEqual(result.report.suites.opencode.counts, { tests: 3, suites: 0, pass: 3, fail: 0, cancelled: 0, skipped: 0, todo: 0 });
});
test('adapter evidence consumes actual Node24 TAP including nested tests', () => {
  const dir = mkdtempSync(join(tmpdir(), 'adapter-tap-'));
  try {
    const path = join(dir, 'probe.mjs');
    writeFileSync(path, "import { test } from 'node:test';\n"
      + piRequired.map(name => `test(${JSON.stringify(name)}, async t => { await t.test('nested', () => {}); });\n`).join('')
      + `test(${JSON.stringify(host)}, { skip: ${JSON.stringify(hostReason)} }, () => {});\n`);
    const env = { ...process.env };
    delete env.NODE_TEST_CONTEXT;
    const actual = spawnSync(process.execPath, ['--test', '--test-reporter=tap', path], { encoding: 'utf8', env });
    assert.equal(actual.status, 0, actual.stderr);
    assert.equal(actual.stderr, '');
    const result = adapterSummary(actual.stdout);
    const truncated = adapterSummary(actual.stdout.replace(/^    ok 1 - nested\r?\n/m, ''));
    assert.equal(truncated.status, 1, 'missing nested result must fail completeness');
    assert.equal(truncated.report.coverage, 'incomplete');
    assert.equal(result.status, 0, result.stdout);
    assert.deepEqual(result.report.suites.pi.counts, { tests: 7, suites: 0, pass: 6, fail: 0, cancelled: 0, skipped: 1, todo: 0 });
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
test('adapter evidence rejects failed outcomes, missing execution and unexpected prerequisite skips', () => {
  for (const outcome of ['failure', 'cancelled', 'skipped', '', 'unknown']) {
    const result = adapterSummary(undefined, undefined, outcome);
    assert.equal(result.status, 1);
    assert.equal(result.report.coverage, 'incomplete');
    assert.equal(result.report.suites.pi.counts.pass, 3, 'original evidence retained');
  }
  for (const raw of [null, '', '{"broken":', '{"counts":{"pass":3}}', tap(piRequired).slice(0, -4),
    tap(piRequired).replace('# fail 0', '# fail 1'), tap(piRequired).replace('# pass 3', '# pass 99'),
    tap(piRequired).replace('# tests 3', '# tests 400').replace('# pass 3', '# pass 400'),
    tap(piRequired).replace('1..3', '1..4'), tap(piRequired).replace('ok 2 -', 'ok 1 -'),
    tap(piRequired).replace('ok 1 -', 'not ok 1 -'), tap(piRequired.slice(1)),
    tap(piRequired, [['mandatory schema', 'Go toolchain unavailable']]),
    tap(piRequired, [[host, 'unexpected reason']])]) {
    const result = adapterSummary(raw);
    assert.equal(result.status, 1, raw);
    assert.equal(result.report.coverage, 'incomplete');
  }
});

const event = (Action, Test, Output) => ({ Action, Package: 'example/store', ...(Test && { Test }), ...(Output && { Output }) });
const passed = [event('pass', 'TestPortable'), event('pass')];

test('native summary requires actual successful step outcome after terminal packages', () => {
  const body = job(source, 'macos-native-tests');
  assert.match(body, /name: Run native Go suite\n        id: native-tests\n/);
  const events = [...passed, event('output', 'TestForeign', 'requires Windows external commands\n'), event('skip', 'TestForeign')];
  const success = summarize(events);
  assert.equal(success.status, 0);
  const expected = JSON.parse(success.stdout);
  assert.equal(expected.coverage, 'complete-with-listed-exclusions');
  assert.deepEqual(expected.tests, { pass: 1, fail: 0, skip: 1 });
  assert.deepEqual(expected.packages, { pass: 1, fail: 0, skip: 0 });
  for (const outcome of ['success', 'failure', 'cancelled', 'skipped', '', 'invalid', null]) {
    const result = summarize(events, undefined, outcome);
    const report = JSON.parse(result.stdout);
    assert.equal(result.status, outcome === 'success' ? 0 : 1, outcome);
    assert.deepEqual(report, { ...expected, coverage: outcome === 'success' ? expected.coverage : 'incomplete' });
    assert.equal(result.stderr, '');
    assert.equal(result.markdown, '## Native macOS scope\n\n```json\n' + result.stdout.trim() + '\n```\n');
  }
  assert.match(body, /name: Summarize native scope\n        if: \$\{\{ !cancelled\(\) \}\}\n        env:\n          NATIVE_TEST_OUTCOME: \$\{\{ steps\.native-tests\.outcome \}\}/);
});

test('native summary reports executed tests, packages, failures and exact skip reasons', () => {
  const result = summarize([...passed,
    event('output', 'TestForeign', 'fixture.go:12: native Windows SQLite temporary routing\n'),
    event('skip', 'TestForeign')]);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /"tests":\{"pass":1,"fail":0,"skip":1\}/);
  assert.match(result.stdout, /"packages":\{"pass":1,"fail":0,"skip":0\}/);
  assert.match(result.stdout, /native Windows SQLite temporary routing/);
  assert.match(result.stdout, /"classification":"foreign-os"/);
  const failure = summarize([event('fail', 'TestPortable'), event('fail')]);
  assert.equal(failure.status, 1);
  assert.match(failure.stdout, /"fail":1/);
});

test('native summary rejects absent, malformed, empty and prerequisite-skipped execution', () => {
  for (const result of [summarize(null), summarize([], 'not json'), summarize([]),
    summarize([...passed, event('output', 'TestSymlink', 'symlinks unavailable: denied\n'), event('skip', 'TestSymlink')]),
    summarize([...passed, event('output', 'TestUnknown', 'unexpected skip\n'), event('skip', 'TestUnknown')])]) {
    assert.equal(result.status, 1, result.stdout);
    assert.match(result.stdout, /incomplete/);
  }
});

test('native summary distinguishes actual filesystem capability and optional Pi capture', () => {
  const result = summarize([...passed,
    event('output', 'TestCase', 'filesystem cannot represent case-distinct candidate paths\n'), event('skip', 'TestCase'),
    { ...event('output', 'TestPiInputRealCapture', 'Pi schema prerequisites absent: TypeBox\n'), Package: 'github.com/Gentleman-Programming/engram/v3/internal/mcp' },
    { ...event('skip', 'TestPiInputRealCapture'), Package: 'github.com/Gentleman-Programming/engram/v3/internal/mcp' }]);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /filesystem-capability/);
  assert.match(result.stdout, /optional-pi-capture/);
});

test('native summary distinguishes no-test packages, build failures and interrupted packages', () => {
  const noTests = summarize([...passed, { ...event('output', undefined, '? example/empty [no test files]\n'), Package: 'example/empty' }, { ...event('skip'), Package: 'example/empty' }]);
  assert.equal(noTests.status, 0, noTests.stdout);
  assert.match(noTests.stdout, /no-test-files/);
  const brokenBuild = summarize([...passed, { Action: 'build-fail', ImportPath: 'example/broken' }]);
  assert.equal(brokenBuild.status, 1, brokenBuild.stdout);
  assert.match(brokenBuild.stdout, /example\/broken/);
  const interrupted = summarize([...passed, { Action: 'start', Package: 'example/interrupted' }]);
  assert.equal(interrupted.status, 1, interrupted.stdout);
  assert.match(interrupted.stdout, /example\/interrupted/);
});

for (const reason of ['CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)',
  'requires URL-style CLOUDSTORE_TEST_DSN and disposable PostgreSQL',
  'requires disposable PostgreSQL via CLOUDSTORE_TEST_DSN']) {
  test('native summary excludes PostgreSQL: ' + reason, () => {
    const result = summarize([...passed, event('output', 'TestPostgres', reason + '\n'), event('skip', 'TestPostgres')]);
    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.match(result.stdout, /"classification":"excluded-postgres"/);
  });
}
test('PostgreSQL exclusions do not hide process prerequisites', () => {
  const missing = summarize([...passed, event('output', 'TestProcess', 'process start unavailable\n'), event('skip', 'TestProcess')]);
  assert.equal(missing.status, 1, missing.stdout);
});

test('native isolation resolves the real temporary root before deriving fixture paths', () => {
  const check = text => {
    const body = job(text, 'macos-native-tests');
    const create = body.indexOf('state=$(mktemp -d /tmp/eg-mac.XXXXXX)');
    const physical = body.indexOf('state=$(cd "$state" && pwd -P)');
    const derive = body.indexOf('mkdir -p "$state/home" "$state/tmp" "$state/store"');
    assert.ok(create >= 0 && physical > create && derive > physical, 'physical root before homes, stores and TMPDIR');
    assert.ok(body.indexOf('echo "TMPDIR=$state/tmp"') > physical);
  };
  check(source);
  assert.throws(() => check(source.replace('state=$(cd "$state" && pwd -P)', 'state="$state"')));
});

for (const [reason, classification] of [
  ['set ENGRAM_DASHBOARD_BENCH_DSN to the documented owned fixture', 'excluded-postgres'],
  ['set ENGRAM_PAUSE_BENCH_DSN to the owned measurement fixture', 'excluded-postgres'],
  ['requires Windows external commands', 'foreign-os'],
  ['requires native Windows and Git Bash', 'foreign-os'],
]) {
  test('native summary classifies observed exclusion: ' + reason, () => {
    const events = [...passed, event('output', 'TestExcluded', reason + '\n'), event('skip', 'TestExcluded')];
    const result = summarize(events);
    assert.equal(result.status, 0, result.stdout + result.stderr);
    const report = JSON.parse(result.stdout);
    assert.equal(report.skips[0].classification, classification);
    assert.equal(report.skips[0].reason, reason + '\n');
    assert.deepEqual(report.tests, { pass: 1, fail: 0, skip: 1 });
    const failed = summarize([...events, event('fail', 'TestOriginalFailure'), event('fail')]);
    assert.equal(failed.status, 1, failed.stdout);
    assert.equal(JSON.parse(failed.stdout).tests.fail, 1);
  });
}
