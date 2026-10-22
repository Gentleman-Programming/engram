import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';

const root = new URL('../../', import.meta.url);
const read = path => readFileSync(new URL(path, root), 'utf8').replaceAll('\r\n', '\n').trimEnd();
const workflow = name => read(`.github/workflows/${name}`);
const hash = source => createHash('sha256').update(source).digest('hex');
const permissions = 'permissions:\n  contents: read\n  issues: read\n  pull-requests: read';
const actions = '    types: [opened, edited, labeled, unlabeled, synchronize, reopened]';
// Literal normalized source pins: no Git/history dependency in shallow checkouts or archives.
const claudeHash = '5290578254c215243f49311a71a092c438861c37551e019cc3fe5dc8120965c6';
const jobsHash = '46bb7715dc7ac644959ca7d9f415e85b71a22f87672f3be18924b02dd450380d';

function prClaudeWorkflow() {
  const path = '.github/workflows/pr-claude-plugin-version.yml';
  assert.ok(existsSync(new URL(path, root)), 'PR-only Claude workflow must exist');
  return read(path);
}

function jobs(source) {
  assert.ok(source.includes('\njobs:\n'), 'workflow must define jobs');
  return source.slice(source.indexOf('jobs:'));
}

function rootPermissions(source) {
  assert.ok(source.includes('\npermissions:\n'), 'workflow must declare root permissions');
  return source.slice(source.indexOf('permissions:'), source.indexOf('\njobs:')).trimEnd();
}

function triggers(source) {
  return source.slice(source.indexOf('\non:\n') + 1, source.indexOf('\npermissions:')).trimEnd();
}

test('retirement removes the transitional API-publisher workflow, not its dormant helpers', () => {
  assert.equal(existsSync(new URL('.github/workflows/issue-validation.yml', root)), false,
    'transitional issue-validation.yml publisher must be absent');
  for (const path of ['issue-check-publisher.mjs', 'issue-check-publisher.test.mjs', 'issue-validation.mjs']) {
    assert.ok(existsSync(new URL(`.github/scripts/${path}`, root)), `${path} must remain`);
  }
});

test('existing PR Validation identity is merge-group-only with unchanged permissions', () => {
  const source = workflow('pr-check.yml');
  assert.equal(triggers(source), 'on:\n  merge_group:\n    types: [checks_requested]',
    'remove only the legacy pull_request subscription; retain merge_group');
  assert.ok(source.startsWith('name: PR Validation\n'));
  assert.equal(rootPermissions(source), permissions);
});

test('complete original merge-group jobs preserve Claude, resolver, readiness and aggregation', () => {
  const source = workflow('pr-check.yml');
  assert.equal(hash(jobs(source)), jobsHash, 'all original jobs must remain byte-identical after LF normalization');
  const claude = source.slice(source.indexOf('  check-claude-plugin-version:'), source.indexOf('\n  check-issue-reference:')).trimEnd();
  assert.equal(hash(claude), claudeHash);
});

test('new Claude workflow subscribes to exactly the original six PR actions, never merge groups', () => {
  const source = prClaudeWorkflow();
  assert.equal(triggers(source), `on:\n  pull_request:\n${actions}`);
  assert.equal(rootPermissions(source), permissions);
});

test('new PR workflow contains only the verbatim original Claude enforcement job', () => {
  const source = prClaudeWorkflow();
  assert.equal(hash(jobs(source).slice('jobs:\n'.length)), claudeHash,
    'copy Claude job verbatim without skipped duplicate issue jobs or job-level retirement');
});

test('native policy, selective router and CI retain their full normalized source contracts', () => {
  const pins = {
    'issue-policy.yml': 'e51537f2401a4d32ba773d0336db9d0271a7d23f09ef7f7ebd163c5699db79f8',
    'issue-refresh-router.yml': 'c588dde34d9396a05f0afdb61941bb60144d23adcd51bd0f11f11c235b8d4878',
    'ci.yml': '4eef2d68552b2186ece74b4e7de7c10bd3f637f318a670cdb707cb176ad5792a',
  };
  for (const [name, expected] of Object.entries(pins)) {
    assert.equal(hash(workflow(name)), expected, name);
  }
});
