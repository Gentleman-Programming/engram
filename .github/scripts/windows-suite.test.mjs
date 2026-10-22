import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const workflow = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const job = workflow.match(/^  windows-unit-tests:\n[\s\S]*?(?=^  [\w-]+:|$(?![\s\S]))/m)?.[0];

test('Windows full suite is independent, observational, and aligned with Linux', () => {
  assert.ok(job, 'missing Windows full-suite job');
  assert.match(job, /name: Windows Unit Tests \(observational\)/);
  assert.match(job, /runs-on: windows-latest/);
  assert.match(job, /if: github.event_name != 'merge_group'/);
  assert.match(job, /ref: \$\{\{ github.sha \}\}/);
  const linux = workflow.match(/^  unit-tests:\n[\s\S]*?(?=^  [\w-]+:)/m)[0];
  assert.equal(job.match(/go-version: "([^"]+)"/)[1], linux.match(/go-version: "([^"]+)"/)[1]);
  assert.match(job, /run: go test \.\/\.\.\./);
  assert.doesNotMatch(job, /continue-on-error|needs:|GOPROXY:|TEMP:|TMP:| -skip| -run|\|\| true/);
});

test('Windows evidence records checkout, toolchain, prerequisites, and actual test outcome', () => {
  assert.ok(job, 'missing Windows full-suite job');
  for (const evidence of ['git rev-parse HEAD', 'go version', 'go env', 'bash --version', 'node --version', '$PSVersionTable', 'steps.suite.outcome']) {
    assert.ok(job.includes(evidence), `missing evidence: ${evidence}`);
  }
  assert.match(job, /id: suite\n\s+run: go test \.\/\.\.\./);
  assert.match(job, /if: always\(\)/);
  assert.match(job, /GITHUB_STEP_SUMMARY/);
});
