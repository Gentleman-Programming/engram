import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';

const read = path => readFileSync(new URL(`../workflows/${path}`, import.meta.url), 'utf8').replaceAll('\r\n', '\n');

test('transitional API publisher is retired; native PR metadata and queue paths remain separate', () => {
  assert.equal(existsSync(new URL('../workflows/issue-validation.yml', import.meta.url)), false);
  assert.match(read('issue-policy.yml'), /pull_request_target:/);
  assert.doesNotMatch(read('issue-policy.yml'), /^  merge_group:/m);
  assert.match(read('pr-check.yml'), /merge_group:\s+types: \[checks_requested\]/);
  assert.doesNotMatch(read('pr-check.yml'), /^  pull_request:/m);
  assert.match(read('pr-claude-plugin-version.yml'), /pull_request:\s+types: \[opened, edited, labeled, unlabeled, synchronize, reopened\]/);
  assert.doesNotMatch(read('pr-claude-plugin-version.yml'), /^  merge_group:|^  check-issue-/m);
});

test('retained queue validation pins qualified references and readiness guards', () => {
  // Approved #1501 baseline with only the legacy PR trigger removed for #1810.
  // Preserve historical readiness exclusions here; the retirement suite separately
  // pins the COMPLETE jobs section, including those guards, without exclusions.
  const bytes = Buffer.from(read('pr-check.yml').replace(/^                  if \(!Array\.isArray\(issue\.labels\).+\n                    throw new Error\('issue labels are malformed'\);\n                  }\n/m, '').replace(/^                  if \(!Array\.isArray\(issue\.assignees\).+\n                    throw new Error\('issue assignees are malformed'\);\n                  }\n                  if \(issue\.assignees\.length === 0\) \{\n                    errors\.push\(.+\);\n                  }\n/m, ''));
  const header = Buffer.from(`blob ${bytes.length}\0`);
  assert.equal(createHash('sha1').update(header).update(bytes).digest('hex'), 'ea1fd6735fd16c43e84a253bcdbeb02ee666ef7b');
});

test('existing policy helper CI runs new regression suites without renaming checks', () => {
  const source = read('ci.yml');
  assert.match(source, /name: Policy Helper Tests/);
  assert.ok(source.includes('run: node --test .github/scripts/merge-queue.test.mjs .github/scripts/label-policy.test.mjs .github/scripts/transient-artifacts.test.mjs .github/scripts/pr-size-notice.test.mjs'));
  assert.match(source, /name: Run issue policy helper tests\s+run: node --test \.github\/scripts\/issue-\*\.test\.mjs/);
  assert.match(source, /policy-helper-tests:\s+name: Policy Helper Tests\s+runs-on: ubuntu-latest/);
});
