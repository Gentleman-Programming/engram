import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const ciWorkflow = fs.readFileSync('.github/workflows/ci.yml', 'utf8');
const prWorkflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
const labelWorkflow = fs.readFileSync('.github/workflows/pr-label-check.yml', 'utf8');
const contributing = fs.readFileSync('CONTRIBUTING.md', 'utf8');

function assertMergeGroupTrigger(workflow, name) {
  assert.match(
    workflow,
    /^  merge_group:\r?\n    types: \[checks_requested\]$/m,
    `${name} must handle merge_group checks_requested events`,
  );
}

function jobBody(workflow, job) {
  const match = workflow.match(new RegExp(`^  ${job}:\\r?\\n([\\s\\S]*?)(?=^  [a-z0-9-]+:|(?![\\s\\S]))`, 'm'));
  assert.ok(match, `expected ${job} job`);
  return match[0];
}

test('runs the required CI checks against merge-group commits', () => {
  assertMergeGroupTrigger(ciWorkflow, 'CI');

  for (const job of ['unit-tests', 'e2e-tests', 'plugin-tests']) {
    assert.match(
      ciWorkflow,
      new RegExp(`  ${job}:\\r?\\n    name: (?:Unit Tests|E2E Tests|Plugin Tests)[\\s\\S]*?ref: \\$\\{\\{ github\\.sha \\}\\}`),
      `${job} must check out the merge-group SHA`,
    );
  }

  for (const job of ['lint', 'windows-setup-test', 'wrapper-tests-windows']) {
    assert.match(
      jobBody(ciWorkflow, job),
      /if: github\.event_name != 'merge_group'/,
      `${job} must not run for merge groups`,
    );
  }

  assert.match(ciWorkflow, /name: Performance Ratchet\r?\n    if: github\.event_name == 'push' && github\.ref == 'refs\/heads\/main'/);
});

function assertSingleContext(workflow, context) {
  const matches = workflow.match(new RegExp(`name: ${context.replace('*', '\\*')}`, 'g')) || [];
  assert.equal(matches.length, 1, `${context} must have exactly one job`);
}

function assertEventGatedStep(job, step, events) {
  const condition = events.map((event) => `github\\.event_name == '${event}'`).join(' \\|\\| ');
  assert.match(
    job,
    new RegExp(`- name: ${step}\\r?\\n        if: ${condition}`),
    `${step} must run for ${events.join(' and ')}`,
  );
}

test('validates current queued PRs within the canonical metadata jobs', () => {
  assertMergeGroupTrigger(prWorkflow, 'PR Validation');
  assertMergeGroupTrigger(labelWorkflow, 'PR Label Policy');

  const issueReference = jobBody(prWorkflow, 'check-issue-reference');
  const issueApproved = jobBody(prWorkflow, 'check-issue-approved');
  const labelPolicy = jobBody(labelWorkflow, 'check-label-policy');

  for (const [workflow, context] of [
    [prWorkflow, 'Check Issue Reference'],
    [prWorkflow, 'Check Issue Has status:approved'],
    [labelWorkflow, 'Check PR Has type:* Label'],
  ]) {
    assertSingleContext(workflow, context);
  }

  for (const job of [issueReference, issueApproved, labelPolicy]) {
    assert.doesNotMatch(job, /Admit merge group/);
    assert.doesNotMatch(job, /merge_group\.head_ref/);
    assert.match(job, /github\.paginate\(/);
    assert.match(job, /github\.rest\.repos\.listPullRequestsAssociatedWithCommit/);
    assert.match(job, /commit_sha: context\.payload\.merge_group\.head_sha/);
    assert.match(job, /new Set\(pulls\.map\(\(pull\) => pull\.number\)\)/);
    assert.match(job, /could not resolve associated pull requests/);
    assert.match(job, /github\.rest\.pulls\.get\(\{/);
  }

  assert.match(issueReference, /for \(const pull of pulls\)/);
  assert.match(issueApproved, /for \(const pull of pulls\)/);
  assert.match(labelPolicy, /for \(const pull of pulls\)/);
  assert.match(labelPolicy, /validateLabels\(policy, pull\.labels\.map\(\(label\) => label\.name\), 'pull-request'\)/);

  assertEventGatedStep(issueReference, 'Verify PR links an issue', ['pull_request', 'merge_group']);
  assertEventGatedStep(issueApproved, 'Verify linked issue is approved', ['pull_request', 'merge_group']);
  assertEventGatedStep(labelPolicy, 'Check out trusted base revision', ['pull_request_target', 'merge_group']);
  assertEventGatedStep(labelPolicy, 'Validate canonical PR labels', ['pull_request_target', 'merge_group']);
  assert.match(labelPolicy, /github\.event\.merge_group\.base_sha/);
  assert.match(
    labelPolicy,
    /group: \$\{\{ github\.workflow \}\}-type-label-\$\{\{ github\.event\.pull_request\.number \|\| github\.run_id \}\}/,
  );
});

test('documents the active required contexts and safe merge queue activation', () => {
  const requiredContexts = [
    'E2E Tests',
    'Unit Tests',
    'Plugin Tests',
    'Check Issue Has status:approved',
    'Check Issue Reference',
    'Check PR Has type:* Label',
  ];

  for (const context of requiredContexts) {
    assert.match(contributing, new RegExp(`\\*\\*${context.replace('*', '\\*')}\\*\\*`));
  }

  assert.match(contributing, /Merge this compatibility PR first/i);
  assert.match(contributing, /`main`-scoped merge queue/i);
  assert.match(contributing, /one concurrent build/i);
  assert.match(contributing, /squash merge/i);
  assert.match(contributing, /disabl(?:e|ing) (?:or removing )?only that queue rule/i);
});
