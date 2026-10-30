import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');

test('Race Tests provides unconditional pinned Node 24 before the full race suite', () => {
  const race = source.match(/^  race-tests:\n([\s\S]*?)(?=^  [\w-]+:|$(?![\s\S]))/m)?.[1];
  assert.ok(race, 'race-tests job must exist');
  const setup = '      - name: Set up Node.js\n'
    + '        uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5\n'
    + '        with:\n          node-version: "24"\n';
  const command = '      - name: Run full race suite with elapsed time\n'
    + '        run: /usr/bin/time -p go test -race -timeout=20m ./...\n';
  assert.ok(race.includes(setup), 'Race Tests must provision pinned Node 24');
  assert.ok(race.includes(command), 'full race command must remain unchanged');
  assert.ok(race.indexOf(setup) < race.indexOf(command), 'Node setup must precede race tests');
  assert.doesNotMatch(race, /^\s*(?:if|continue-on-error):/m, 'job and steps must not bypass tests');
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
