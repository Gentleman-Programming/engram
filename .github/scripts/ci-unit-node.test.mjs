import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');

function raceNode(text) {
  const race = text.match(/^  race-tests:\n([\s\S]*?)(?=^  [\w-]+:|$(?![\s\S]))/m)?.[1];
  assert.ok(race, 'race-tests job must exist');
  const setup = '      - name: Set up Node.js\n'
    + '        uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5\n'
    + '        with:\n          node-version: "24"\n';
  const command = '      - name: Run full race suite with elapsed time\n'
    + '        run: /usr/bin/time -p go test -race -timeout=20m ./...\n';
  assert.ok(race.includes(setup), 'Race Tests must provision pinned Node 24');
  assert.ok(race.includes(command), 'full race command must remain unchanged');
  assert.ok(race.indexOf(setup) < race.indexOf(command), 'Node setup must precede race tests');
  const steps = race.match(/^      - name: .*\n[\s\S]*?(?=^      - name: |$(?![\s\S]))/gm) ?? [];
  const saves = steps.filter(step => step.includes('uses: actions/cache/save@'));
  assert.equal(saves.length, 1, 'one explicit cache-save step');
  const guard = "        if: ${{ success() && github.repository == 'Gentleman-Programming/engram' && github.event_name == 'push' && github.ref == 'refs/heads/main' && steps.race-cache-restore.outputs.cache-hit != 'true' }}\n";
  assert.ok(saves[0].includes(guard), 'only the approved successful trusted-main cache guard');
  const unconditional = race.replace(saves[0], () => saves[0].replace(guard, ''));
  assert.doesNotMatch(unconditional, /^\s*(?:if|continue-on-error):/m, 'job and execution steps must not bypass tests');
}

test('Race Tests provides unconditional pinned Node 24 before the full race suite', () => raceNode(source));
test('Race Node contract permits only the validated cache writer condition', () => {
  raceNode(source);
  const original = source.match(/^  race-tests:\n[\s\S]*?(?=^  [\w-]+:)/m)[0];
  const steps = original.match(/^      - name: .*\n[\s\S]*?(?=^      - name: |$(?![\s\S]))/gm);
  for (const step of steps) {
    for (const policy of ['if: false', 'continue-on-error: true']) {
      const changed = step.replace(/^(      - name: .*\n)/, `$1        ${policy}\n`);
      const candidate = source.replace(original, () => original.replace(step, () => changed));
      assert.equal(candidate.replace(changed, () => step), source, 'Bash quoting and all other stages survive literal step substitution');
      assert.throws(() => raceNode(candidate), undefined, step.split('\n')[0] + policy);
    }
  }
  assert.throws(() => raceNode(source.replace(original, () => original.replace("success() &&", "always() &&"))));
  assert.throws(() => raceNode(source.replace(original, () => original.replace('    name: Go Race Tests', '    if: false\n    name: Go Race Tests'))));
});

test('Unit Tests provides unconditional pinned Node 24 before the full Go suite', () => {
  const unit = source.match(/^  unit-tests:\n([\s\S]*?)(?=^  [\w-]+:|$(?![\s\S]))/m)?.[1];
  assert.ok(unit, 'unit-tests job must exist');
  const setup = '      - name: Set up Node.js\n'
    + '        uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5\n'
    + '        with:\n          node-version: "24"\n';
  const command = '      - name: Run unit tests\n        run: go test ./...\n';
  assert.ok(unit.includes(setup), 'Unit Tests must provision pinned Node 24');
  assert.ok(unit.includes(command), 'full go test command must remain unchanged');
  assert.ok(unit.indexOf(setup) < unit.indexOf(command), 'Node setup must precede Go tests');
  assert.doesNotMatch(unit, /^\s*(?:if|continue-on-error):/m, 'job and steps must not bypass tests');
});
