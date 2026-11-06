import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const ordinary = "github.event_name == 'pull_request' && !startsWith(github.head_ref, 'ci/proof-') && vars.ISSUE_CI_PROOF_CI == ''";

// Evaluate only the policy's fixed expression subset, not arbitrary Actions syntax.
// This checks local routing; GitHub admission/cancellation still needs hosted proof.
function policy(overrides = {}, vars = {}) {
  const block = source.match(/^concurrency:\n([\s\S]*?)(?=^\S)/m)?.[1];
  assert.ok(block, 'CI must define workflow-level concurrency');
  const group = block.match(/^  group: (.+)$/m)?.[1];
  const cancel = block.match(/^  cancel-in-progress: \$\{\{ (.+) \}\}$/m)?.[1];
  assert.equal(group, `\${{ github.workflow }}-\${{ ${ordinary} && format('pr-{0}', github.event.pull_request.number) || format('run-{0}-{1}', github.run_id, github.run_attempt) }}`);
  assert.equal(cancel, ordinary, 'group admission and cancellation must use the same predicate');
  const github = { workflow: 'CI', event_name: 'pull_request', head_ref: 'feature/example',
    event: { pull_request: { number: 42 } }, run_id: 100, run_attempt: 1, ...overrides };
  const scope = { ISSUE_CI_PROOF_CI: '', ...vars };
  const startsWith = (value, prefix) => value.toLowerCase().startsWith(prefix.toLowerCase());
  const format = (template, ...args) => template.replace(/\{(\d+)\}/g, (_, n) => String(args[n]));
  const evaluate = expression => new Function('github', 'vars', 'startsWith', 'format',
    `return (${expression});`)(github, scope, startsWith, format);
  return { group: group.replace(/\$\{\{ (.*?) \}\}/g, (_, expression) => evaluate(expression)),
    cancel: evaluate(cancel) };
}

const sameGroup = (a, b) => a.group.toLowerCase() === b.group.toLowerCase();

test('all reserved proof prefixes have unique run groups, even malformed routing', () => {
  for (const head_ref of ['ci/proof-2096-case', 'ci/proof-', 'ci/proof-0-bad', 'CI/PROOF-2096-case']) {
    const first = policy({ head_ref });
    assert.deepEqual(first, { group: 'CI-run-100-1', cancel: false });
    assert.equal(sameGroup(first, policy({ head_ref, run_id: 101 })), false);
    assert.equal(sameGroup(first, policy({ head_ref, run_attempt: 2 })), false);
    assert.equal(sameGroup(first, policy()), false);
  }
});

test('any nonempty raw selector isolates runs without parsing or authorizing it', () => {
  for (const raw of ['{"enabled":true}', '{"enabled":false}', 'false', 'not-json', ' ',
    '{"enabled":true,"target":{"repo":"other/repo"}}']) {
    const vars = { ISSUE_CI_PROOF_CI: raw };
    const first = policy({}, vars);
    assert.deepEqual(first, { group: 'CI-run-100-1', cancel: false });
    assert.equal(sameGroup(first, policy({ run_id: 101 }, vars)), false);
    assert.equal(sameGroup(first, policy({ run_attempt: 2 }, vars)), false);
    assert.equal(sameGroup(first, policy()), false);
  }
});

test('nonempty raw selector cannot enable protected event cancellation', () => {
  for (const event_name of ['merge_group', 'push']) {
    assert.equal(policy({ event_name, head_ref: '', event: {} }, { ISSUE_CI_PROOF_CI: 'not-json' }).cancel, false);
  }
});

test('empty selector keeps ordinary PR verification enabled', () => {
  assert.deepEqual(policy({}, { ISSUE_CI_PROOF_CI: '' }), { group: 'CI-pr-42', cancel: true });
});

// Ordinary routing, events, default permissions and suite discovery are owned by
// ci-technical-contracts.test.mjs; exact proof admission stays here.
