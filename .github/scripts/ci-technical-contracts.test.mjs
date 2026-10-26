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
  assert.doesNotMatch(body, /continue-on-error|needs:|\n\s+if:|\|\| true| -run | -short| -skip|secrets\.|: write|environment:/);
  for (const action of body.matchAll(/uses: (.+)/g)) assert.match(action[1], /@[a-f0-9]{40} # v\d+$/);
}

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
      assert.ok(source.includes(before), `fixture target: ${before}`);
      assert.throws(() => check(source.replace(before, after)), undefined, `${before} -> ${after}`);
    }
  });
}

test('verification rejects missing setup and reordered preflight/build stages', () => {
  const setup = '      - name: Set up Go\n        uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6\n        with:\n          go-version: "1.26.10"\n';
  const unit = job(source, 'unit-tests');
  assert.ok(unit.includes(setup));
  assert.throws(() => verification(source.replace(unit, unit.replace(setup, ''))));
  for (const [first, second] of [['go mod tidy -diff', 'make templ'], ['make templ', 'make deadcode-check'],
    ['make deadcode-check', 'go test ./...'], ['go test ./...', 'go build -o engram-selftest ./cmd/engram && ./engram-selftest test --quick']]) {
    const changed = unit.replace(first, '__SWAP__').replace(second, first).replace('__SWAP__', second);
    assert.throws(() => verification(source.replace(unit, changed)), undefined, `${first} before ${second}`);
  }
});
