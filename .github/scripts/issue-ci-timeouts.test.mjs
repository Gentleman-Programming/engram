import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const workflow = read('../workflows/ci.yml');
const budgets = {
  lint: 15,
  'policy-helper-tests': 10,
  'unit-tests': 20,
  'e2e-tests': 15,
  'windows-setup-test': 20,
  'wrapper-tests-windows': 15,
  'obsidian-build': 10,
  'perf-ratchet': 20,
};
const job = (source, id) => source.match(
  new RegExp(`^  ${id}:\\n[\\s\\S]*?(?=^  [a-z][\\w-]*:|$(?![\\s\\S]))`, 'm'),
)?.[0] ?? '';

function assertBudget(source, id, minutes) {
  const body = job(source, id);
  assert.ok(body, `${id}: job must exist`);
  const values = [...body.matchAll(/^    timeout-minutes: (.+)$/gm)].map(match => match[1]);
  assert.deepEqual(values, [String(minutes)], `${id}: exact job-level ceiling required`);
}

for (const [id, minutes] of Object.entries(budgets)) {
  test(`${id} has the accepted ${minutes}m outer ceiling`, () => {
    assertBudget(workflow, id, minutes);
  });

  test(`${id} rejects missing, wrong and step-only ceilings`, () => {
    const fixture = `jobs:\n  ${id}:\n    timeout-minutes: ${minutes}\n    steps:\n      - run: echo fixture\n`;
    assertBudget(fixture, id, minutes);
    const line = `    timeout-minutes: ${minutes}\n`;
    const invalid = [
      fixture.replace(line, ''),
      fixture.replace(line, `    timeout-minutes: ${minutes + 1}\n`),
      fixture.replace(line, '').replace('      - run:', `      - timeout-minutes: ${minutes}\n        run:`),
    ];
    for (const source of invalid) {
      assert.throws(() => assertBudget(source, id, minutes), /exact job-level ceiling required/);
    }
  });
}

test('previously bounded jobs retain their exact outer ceilings', () => {
  for (const [id, minutes] of Object.entries({
    'race-tests': 30,
    govulncheck: 15,
    actionlint: 15,
    'claude-jq16-compatibility': 10,
    'postgres-tests': 20,
    'plugin-tests': 10,
  })) assertBudget(workflow, id, minutes);
});

test('the contributor timeout table matches all eight accepted ceilings', () => {
  const docs = read('../../CONTRIBUTING.md');
  for (const [id, minutes] of Object.entries(budgets)) {
    assert.ok(docs.includes(`| \`${id}\` | ${minutes} |`), `${id}: documented ceiling`);
  }
});
