import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';

const read = (path) => readFileSync(new URL(`../workflows/${path}`, import.meta.url), 'utf8');
const workflow = () => read('issue-validation.yml');

test('covers trusted PR and approval-label events only; never grants writes to merge-group code', () => {
  const source = workflow();
  assert.match(source, /pull_request_target:\s+types: \[opened, edited, labeled, unlabeled, synchronize, reopened\]/);
  assert.match(source, /issues:\s+types: \[labeled, unlabeled\]/);
  const triggers = source.split('on:')[1].split('permissions:')[0];
  assert.deepEqual([...triggers.matchAll(/^  (\w+):/gm)].map((match) => match[1]), ['pull_request_target', 'issues']);
  assert.doesNotMatch(source, /merge_group|workflow_run/);
  assert.match(read('pr-check.yml'), /merge_group:\s+types: \[checks_requested\]/);
  assert.match(source, /if: github.event_name != 'issues' \|\| \(github.event.issue.pull_request == null && github.event.label.name == 'status:approved'\)/);
});

test('trusted immutable checkout, no persisted credentials or PR execution', () => {
  const source = workflow();
  assert.match(source, /uses: actions\/checkout@d23441a48e516b6c34aea4fa41551a30e30af803/);
  assert.match(source, /ref: \$\{\{ github.workflow_sha \}\}/);
  assert.match(source, /repository: \$\{\{ github.repository \}\}/);
  assert.match(source, /persist-credentials: false/);
  assert.match(source, /uses: actions\/github-script@ed597411d8f924073f98dfc5c65a23a2325f34cd/);
  assert.doesNotMatch(source, /\brun:|\bshell:|pull_request\.head|cancel-in-progress|concurrency:|workflow_dispatch|schedule:/);
  const script = source.split('script: |')[1];
  assert.ok(script);
  assert.doesNotMatch(script, /\$\{\{/);
});

test('empty global permissions and only justified job scopes', () => {
  const source = workflow();
  for (const newline of ['\n', '\r\n']) {
    assert.match(source.replace(/\r?\n/g, newline), /\r?\npermissions: \{\}\r?\n/);
  }
  const scopes = [...source.matchAll(/^      ([\w-]+): (read|write)$/gm)].map((match) => `${match[1]}:${match[2]}`).sort();
  assert.deepEqual(scopes, ['checks:write', 'contents:read', 'issues:read', 'pull-requests:read']);
});

test('static trusted helpers evaluate before publishing and exceptions fail workflow', () => {
  const source = workflow();
  assert.ok(source.includes('import(`${process.env.GITHUB_WORKSPACE}/.github/scripts/issue-validation.mjs`)'));
  assert.ok(source.includes('import(`${process.env.GITHUB_WORKSPACE}/.github/scripts/issue-check-publisher.mjs`)'));
  assert.match(source, /const evaluations = await evaluateIssueValidation\(github, context\);\s+await publishIssueChecks\(github, context.repo, evaluations\);/);
  assert.match(source, /catch \(error\) \{\s+core.setFailed\(error.message\);/);
});

test('bootstrap pins PR validation including approved qualified references and readiness guards', () => {
  // Pin the approved #1501 baseline, retaining the existing readiness-guard exclusions.
  // Git's blob hash normalizes checkout line endings on Windows.
  const bytes = Buffer.from(read('pr-check.yml').replaceAll('\r\n', '\n').replace(/^                  if \(!Array\.isArray\(issue\.labels\).+\n                    throw new Error\('issue labels are malformed'\);\n                  }\n/m, '').replace(/^                  if \(!Array\.isArray\(issue\.assignees\).+\n                    throw new Error\('issue assignees are malformed'\);\n                  }\n                  if \(issue\.assignees\.length === 0\) \{\n                    errors\.push\(.+\);\n                  }\n/m, ''));
  const header = Buffer.from(`blob ${bytes.length}\0`);
  assert.equal(createHash('sha1').update(header).update(bytes).digest('hex'), 'bfeb81f457cee4904d210810cd588460519ca16d');
});

test('existing policy helper CI runs new regression suites without renaming checks', () => {
  const source = read('ci.yml');
  assert.match(source, /name: Policy Helper Tests/);
  assert.ok(source.includes('run: node --test .github/scripts/merge-queue.test.mjs .github/scripts/label-policy.test.mjs .github/scripts/transient-artifacts.test.mjs .github/scripts/pr-size-notice.test.mjs'));
  assert.match(source, /name: Run issue policy helper tests\s+run: node --test \.github\/scripts\/issue-\*\.test\.mjs/);
  // Ubuntu's shell expands only files present in the slice's checkout.
  assert.match(source, /policy-helper-tests:\s+name: Policy Helper Tests\s+runs-on: ubuntu-latest/);
});
