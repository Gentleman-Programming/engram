import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import { aggregatePullRequestResults, resolveAssociatedPullRequests } from './merge-queue.mjs';
import { loadLabelPolicy, validateLabels } from './label-policy.mjs';

const ciWorkflow = fs.readFileSync('.github/workflows/ci.yml', 'utf8');
const prWorkflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
const labelWorkflow = fs.readFileSync('.github/workflows/pr-label-check.yml', 'utf8');
const contributing = fs.readFileSync('CONTRIBUTING.md', 'utf8');

function associatedPullsGitHub({ associated, pulls, paginateError, pullError } = {}) {
  const listAssociated = () => {};
  const getPull = () => {};
  const fetches = [];
  return {
    fetches,
    github: {
      paginate: async (method, params) => {
        assert.equal(method, listAssociated);
        assert.deepEqual(params, {
          owner: 'Gentleman-Programming',
          repo: 'engram',
          commit_sha: 'merge-group-sha',
        });
        if (paginateError) throw paginateError;
        return associated;
      },
      rest: {
        repos: { listPullRequestsAssociatedWithCommit: listAssociated },
        pulls: {
          get: async (params) => {
            fetches.push(params);
            if (pullError) throw pullError;
            return { data: pulls.get(params.pull_number) };
          },
        },
      },
    },
  };
}

const repository = { owner: 'Gentleman-Programming', repo: 'engram', commitSha: 'merge-group-sha' };

test('resolves one associated PR from current API metadata', async () => {
  const currentPull = { number: 42, body: 'Closes #926', labels: [{ name: 'type:chore' }] };
  const fixture = associatedPullsGitHub({
    associated: [{ number: 42 }],
    pulls: new Map([[42, currentPull]]),
  });

  assert.deepEqual(await resolveAssociatedPullRequests(fixture.github, repository), [currentPull]);
  assert.deepEqual(fixture.fetches, [{ owner: 'Gentleman-Programming', repo: 'engram', pull_number: 42 }]);
});

test('resolves multiple associated PRs from current API metadata', async () => {
  const first = { number: 42 };
  const second = { number: 43 };
  const fixture = associatedPullsGitHub({
    associated: [{ number: 42 }, { number: 43 }],
    pulls: new Map([[42, first], [43, second]]),
  });

  assert.deepEqual(await resolveAssociatedPullRequests(fixture.github, repository), [first, second]);
  assert.deepEqual(fixture.fetches.map(({ pull_number }) => pull_number), [42, 43]);
});

test('deduplicates paginated associated PRs in first-seen order', async () => {
  const fixture = associatedPullsGitHub({
    associated: [{ number: 43 }, { number: 42 }, { number: 43 }, { number: 44 }, { number: 42 }],
    pulls: new Map([[42, { number: 42 }], [43, { number: 43 }], [44, { number: 44 }]]),
  });

  assert.deepEqual(
    (await resolveAssociatedPullRequests(fixture.github, repository)).map(({ number }) => number),
    [43, 42, 44],
  );
  assert.deepEqual(fixture.fetches.map(({ pull_number }) => pull_number), [43, 42, 44]);
});

test('rejects a merge group with no associated PRs', async () => {
  const fixture = associatedPullsGitHub({ associated: [], pulls: new Map() });

  await assert.rejects(
    resolveAssociatedPullRequests(fixture.github, repository),
    /could not resolve associated pull requests/,
  );
});

test('propagates associated-PR pagination failures', async () => {
  const failure = new Error('GitHub pagination failed');
  const fixture = associatedPullsGitHub({ paginateError: failure });

  await assert.rejects(resolveAssociatedPullRequests(fixture.github, repository), (error) => error === failure);
});

test('propagates current PR fetch failures', async () => {
  const failure = new Error('GitHub pull lookup failed');
  const fixture = associatedPullsGitHub({
    associated: [{ number: 42 }],
    pulls: new Map(),
    pullError: failure,
  });

  await assert.rejects(resolveAssociatedPullRequests(fixture.github, repository), (error) => error === failure);
});

test('aggregates mixed valid and invalid pull-request results', async () => {
  const pulls = [{ number: 42 }, { number: 43 }, { number: 44 }];

  assert.deepEqual(
    await aggregatePullRequestResults(pulls, async (pull) => (
      pull.number === 43 ? ['missing closing issue', 'missing approval'] : []
    )),
    ['PR #43: missing closing issue', 'PR #43: missing approval'],
  );
});

test('propagates validator failures', async () => {
  const failure = new Error('issue lookup failed');

  await assert.rejects(
    aggregatePullRequestResults([{ number: 42 }], () => { throw failure; }),
    (error) => error === failure,
  );
});

test('aggregates mixed valid and invalid labels with the canonical policy', async () => {
  const policy = loadLabelPolicy();
  const pulls = [
    { number: 42, labels: [{ name: 'type:chore' }] },
    { number: 43, labels: [{ name: 'size:exception' }] },
  ];

  assert.deepEqual(
    await aggregatePullRequestResults(pulls, (pull) => (
      validateLabels(policy, pull.labels.map((label) => label.name), 'pull-request').errors
    )),
    ['PR #43: type:* requires exactly one type:* label'],
  );
});

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

function assertTrustedBaseCheckout(job) {
  assert.match(job, /name: Check out trusted base revision/);
  assert.match(job, /uses: actions\/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6/);
  assert.match(job, /github\.event\.merge_group\.base_sha \|\| github\.event\.pull_request\.base\.sha/);
  assert.match(job, /persist-credentials: false/);
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

test('validates current queued PRs through trusted merge-queue helpers', () => {
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
    assertTrustedBaseCheckout(job);
    assert.match(
      job,
      /if \(context\.eventName === 'merge_group'\) \{\r?\n              const \{ aggregatePullRequestResults, resolveAssociatedPullRequests \} = await import\(.*merge-queue\.mjs/,
      'merge-queue helper must load only inside the merge-group branch',
    );
    assert.doesNotMatch(
      job,
      /^            const \{ aggregatePullRequestResults, resolveAssociatedPullRequests \} = await import\(.*merge-queue\.mjs/m,
      'ordinary PR validation must not import a helper absent from the trusted base',
    );
    assert.match(job, /resolveAssociatedPullRequests/);
    assert.match(job, /aggregatePullRequestResults/);
    assert.doesNotMatch(job, /github\.paginate\(/);
    assert.doesNotMatch(job, /listPullRequestsAssociatedWithCommit/);
  }

  assertEventGatedStep(issueReference, 'Verify PR links an issue', ['pull_request', 'merge_group']);
  assertEventGatedStep(issueApproved, 'Verify linked issue is approved', ['pull_request', 'merge_group']);
  assertEventGatedStep(labelPolicy, 'Check out trusted base revision', ['pull_request_target', 'merge_group']);
  assertEventGatedStep(labelPolicy, 'Validate canonical PR labels', ['pull_request_target', 'merge_group']);
  assert.match(labelPolicy, /validateLabels\(policy, pull\.labels\.map\(\(label\) => label\.name\), 'pull-request'\)/);
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
