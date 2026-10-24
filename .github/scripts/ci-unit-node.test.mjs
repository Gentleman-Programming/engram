import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');

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
