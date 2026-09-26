import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import { aggregatePullRequestResults, resolveAssociatedPullRequests } from './merge-queue.mjs';
import { loadLabelPolicy, validateLabels } from './label-policy.mjs';

const ciWorkflow = fs.readFileSync('.github/workflows/ci.yml', 'utf8');
const prWorkflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
const labelWorkflow = fs.readFileSync('.github/workflows/pr-label-check.yml', 'utf8');
const contributing = fs.readFileSync('CONTRIBUTING.md', 'utf8');

function mergeQueueGitHub({ pages = [], pulls = new Map(), graphqlError, pullError } = {}) {
  const queueCalls = [];
  const fetches = [];
  const listAssociated = () => { throw new Error('synthetic commit association must not be queried'); };
  return {
    queueCalls,
    fetches,
    github: {
      graphql: async (query, variables) => {
        assert.match(query, /repository\([^)]*\)\s*\{\s*mergeQueue\(branch:/);
        queueCalls.push(variables);
        if (graphqlError) throw graphqlError;
        const page = pages.find(({ cursor }) => cursor === variables.cursor);
        if (!page) throw new Error(`unexpected merge queue cursor: ${variables.cursor}`);
        const pageInfo = Object.hasOwn(page, 'pageInfo')
          ? page.pageInfo
          : { hasNextPage: Boolean(page.hasNextPage), endCursor: page.endCursor ?? null };
        return {
          repository: {
            mergeQueue: {
              entries: {
                nodes: page.entries,
                pageInfo,
              },
            },
          },
        };
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

const repository = {
  owner: 'Gentleman-Programming',
  repo: 'engram',
  baseRef: 'refs/heads/main',
  headRef: 'refs/heads/gh-readonly-queue/main/pr-1314-0123456789abcdef0123456789abcdef01234567',
};

function queueEntry(position, number) {
  return { position, pullRequest: { number } };
}

test('resolves the #1314 merge group when synthetic commit association is empty', async () => {
  const currentPull = { number: 1314, body: 'Closes #1325', labels: [{ name: 'type:bug' }] };
  const fixture = mergeQueueGitHub({
    pages: [{ cursor: null, entries: [queueEntry(1, 1314)] }],
    pulls: new Map([[1314, currentPull]]),
  });

  assert.deepEqual(await resolveAssociatedPullRequests(fixture.github, repository), [currentPull]);
  assert.deepEqual(fixture.queueCalls, [{ owner: 'Gentleman-Programming', repo: 'engram', baseRef: 'main', cursor: null }]);
  assert.deepEqual(fixture.fetches, [{ owner: 'Gentleman-Programming', repo: 'engram', pull_number: 1314 }]);
});

test('resolves a cumulative group through its tail PR in queue order across pages', async () => {
  const fixture = mergeQueueGitHub({
    pages: [
      { cursor: null, entries: [queueEntry(1, 1312), queueEntry(2, 1313)], hasNextPage: true, endCursor: 'page-2' },
      { cursor: 'page-2', entries: [queueEntry(3, 1314), queueEntry(4, 1315)] },
    ],
    pulls: new Map([[1312, { number: 1312 }], [1313, { number: 1313 }], [1314, { number: 1314 }]]),
  });

  assert.deepEqual(
    (await resolveAssociatedPullRequests(fixture.github, repository)).map(({ number }) => number),
    [1312, 1313, 1314],
  );
  assert.deepEqual(fixture.queueCalls.map(({ cursor }) => cursor), [null, 'page-2']);
  assert.deepEqual(fixture.fetches.map(({ pull_number }) => pull_number), [1312, 1313, 1314]);
});

test('fails closed for malformed queue head refs, absent tails, and empty queues', async () => {
  await assert.rejects(
    resolveAssociatedPullRequests(mergeQueueGitHub().github, { ...repository, baseRef: 'main' }),
    /invalid merge queue base ref/,
  );

  await assert.rejects(
    resolveAssociatedPullRequests(mergeQueueGitHub().github, { ...repository, headRef: 'refs/heads/main' }),
    /invalid merge queue head ref/,
  );

  await assert.rejects(
    resolveAssociatedPullRequests(mergeQueueGitHub({
      pages: [{ cursor: null, entries: [queueEntry(1, 1313)] }],
    }).github, repository),
    /merge queue tail PR #1314 was not found/,
  );

  await assert.rejects(
    resolveAssociatedPullRequests(mergeQueueGitHub({ pages: [{ cursor: null, entries: [] }] }).github, repository),
    /merge queue is empty/,
  );
});

test('fails closed for malformed pagination and propagates GitHub API failures', async () => {
  for (const pageInfo of [undefined, { hasNextPage: 'false' }]) {
    await assert.rejects(
      resolveAssociatedPullRequests(mergeQueueGitHub({
        pages: [{ cursor: null, entries: [queueEntry(1, 1314)], pageInfo }],
      }).github, repository),
      /merge queue response is malformed/,
    );
  }

  await assert.rejects(
    resolveAssociatedPullRequests(mergeQueueGitHub({
      pages: [{ cursor: null, entries: [queueEntry(1, 1314)], pageInfo: { hasNextPage: true } }],
    }).github, repository),
    /merge queue pagination cursor is missing/,
  );

  const graphqlFailure = new Error('GitHub GraphQL failed');
  await assert.rejects(
    resolveAssociatedPullRequests(mergeQueueGitHub({ graphqlError: graphqlFailure }).github, repository),
    (error) => error === graphqlFailure,
  );

  const pullFailure = new Error('GitHub pull lookup failed');
  await assert.rejects(
    resolveAssociatedPullRequests(mergeQueueGitHub({
      pages: [{ cursor: null, entries: [queueEntry(1, 1314)] }],
      pullError: pullFailure,
    }).github, repository),
    (error) => error === pullFailure,
  );
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
    assert.match(job, /resolveAssociatedPullRequests\(github, \{/);
    assert.match(job, /aggregatePullRequestResults/);
    assert.doesNotMatch(job, /github\.paginate\(/);
    assert.doesNotMatch(job, /listPullRequestsAssociatedWithCommit/);
  }

  for (const job of [issueReference, issueApproved]) {
    assert.match(
      job,
      /resolveAssociatedPullRequests\(github, \{\r?\n                  owner: context\.repo\.owner,\r?\n                  repo: context\.repo\.repo,\r?\n                  baseRef: context\.payload\.merge_group\.base_ref,\r?\n                  headRef: context\.payload\.merge_group\.head_ref,\r?\n                \}\)/,
      'PR validation must use the event base and queue head refs',
    );
  }
  assert.match(
    labelPolicy,
    /const \{ base_ref: baseRef, head_ref: headRef \} = context\.payload\.merge_group;\r?\n                const pulls = await resolveAssociatedPullRequests\(github, \{\r?\n                  owner: context\.repo\.owner,\r?\n                  repo: context\.repo\.repo,\r?\n                  baseRef,\r?\n                  headRef,\r?\n                \}\)/,
    'label policy must pass the event base and queue head refs',
  );

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
