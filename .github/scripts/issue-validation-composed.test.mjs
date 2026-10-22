import test from 'node:test';
import assert from 'node:assert/strict';
import { evaluateNativeIssuePolicy } from './issue-native-policy.mjs';

const repo = { owner: 'Gentleman-Programming', repo: 'engram' };
const sha = 'a'.repeat(40);
const names = ['Check Issue Reference', 'Check Issue Has status:approved'];
const workspace = new URL('../../', import.meta.url).href.replace(/\/$/, '');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
// Historical publisher wrapper, retained only to cover dormant helper composition
// and late old-code executions. This is NOT an active workflow or recovery route.
const historicalPublisherWrapper = `
try {
  const { evaluateIssueValidation } = await import(\`\${process.env.GITHUB_WORKSPACE}/.github/scripts/issue-validation.mjs\`);
  const { publishIssueChecks } = await import(\`\${process.env.GITHUB_WORKSPACE}/.github/scripts/issue-check-publisher.mjs\`);
  const evaluations = await evaluateIssueValidation(github, context);
  await publishIssueChecks(github, context.repo, evaluations);
} catch (error) {
  core.setFailed(error.message);
}
`;

async function execute({ action = 'labeled', approved = true, body = 'Closes #9', pr = false,
  unrelated = false, changed = false, fault = '', secondApproved = true,
  assignees = [{ login: 'owner-not-author' }], approvalState, beforeCreate } = {}) {
  const reads = [], attempts = [], stored = [], failures = [];
  let pullReads = 0;
  const pull = { number: 1, state: 'open', body, head: { sha, repo: { full_name: 'untrusted/fork' } } };
  const github = { rest: {
    pulls: {
      list: async (request) => { reads.push(['list', request]); return { data: [{ number: 1 }] }; },
      get: async (request) => {
        reads.push(['pull', request]);
        if (fault === 'metadata') throw new Error('Metadata denied');
        return { data: structuredClone(changed && ++pullReads === 2 ? { ...pull, head: { sha: 'b'.repeat(40) } } : pull) };
      },
    },
    issues: { get: async (request) => {
      reads.push(['issue', request]);
      if (fault === 'issue') throw new Error('Issue unavailable');
      const allowed = request.issue_number === 9 ? (approvalState?.approved ?? approved) : secondApproved;
      return { data: { labels: allowed ? [{ name: 'status:approved' }] : [], assignees } };
    } },
    checks: { create: async (request) => {
      if (beforeCreate) await beforeCreate(structuredClone(request));
      attempts.push(structuredClone(request));
      if (fault === 'create' || (fault === 'later' && attempts.length === 2)) throw new Error('Checks denied');
      const run = { ...request, app: { id: fault === 'binding' ? 42 : 15368 } };
      stored.push(structuredClone(run));
      return { data: run };
    } },
  } };
  const context = { repo, eventName: pr ? 'pull_request_target' : 'issues', payload: {
    action, label: { name: unrelated ? 'priority:high' : 'status:approved' }, issue: { number: 9 },
    pull_request: { number: 1, body: 'Closes #999', head: { sha: 'c'.repeat(40) } },
  } };
  const script = historicalPublisherWrapper;
  await new AsyncFunction('github', 'context', 'core', 'process', script)(github, context,
    { setFailed: (message) => failures.push(message) }, { env: { GITHUB_WORKSPACE: workspace } });
  return { reads, attempts, stored, failures };
}

function requests(conclusions, summaries) {
  return names.map((name, index) => ({ ...repo, name, head_sha: sha, status: 'completed',
    conclusion: conclusions[index], output: { title: name, summary: summaries[index] } }));
}

for (const [action, approved] of [['labeled', true], ['unlabeled', false], ['labeled', false], ['unlabeled', true]]) {
  test(`composed ${action} reconciles current approval ${approved}, not event ordering`, async () => {
    const state = await execute({ action, approved });
    const expected = requests(['success', approved ? 'success' : 'failure'],
      ['Validation passed.', approved ? 'Validation passed.' : 'PR #1: issue #9 lacks status:approved']);
    assert.deepEqual(state.attempts, expected);
    assert.deepEqual(state.stored, expected.map((run) => ({ ...run, app: { id: 15368 } })));
    assert.deepEqual(state.failures, []);
    assert.deepEqual(state.reads, [['list', { ...repo, state: 'open', per_page: 100, page: 1 }],
      ['pull', { ...repo, pull_number: 1 }], ['issue', { ...repo, issue_number: 9 }], ['pull', { ...repo, pull_number: 1 }]]);
  });
}

test('a paused legacy success can publish after a current native failure without changing reevaluation', { timeout: 5000 }, async () => {
  const approvalState = { approved: true };
  const order = [], nativeReads = [], nativeWrites = [];
  let markReady, resumeLegacy;
  const ready = new Promise((resolve) => { markReady = resolve; });
  const resume = new Promise((resolve) => { resumeLegacy = resolve; });
  const legacy = execute({ approvalState, beforeCreate: async (request) => {
    if (request.name === names[1]) {
      order.push('legacy approval snapshot ready');
      markReady(request);
      await resume;
      order.push('legacy approval publication');
    }
  } });
  const pull = { number: 1, state: 'open', body: 'Closes #9',
    head: { sha, repo: { full_name: 'untrusted/fork' } } };
  const github = { rest: {
    pulls: { get: async (request) => {
      nativeReads.push(['pull', request]); return { data: structuredClone(pull) };
    } },
    issues: { get: async (request) => {
      nativeReads.push(['issue', request]);
      return { data: { labels: approvalState.approved ? [{ name: 'status:approved' }] : [],
        assignees: [{ login: 'owner-not-author' }] } };
    } },
    checks: { create: async (request) => {
      nativeWrites.push(request); assert.fail('Native evaluation must remain read-only');
    } },
  } };
  const context = { repo, eventName: 'pull_request_target', payload: {
    action: 'opened', pull_request: { number: 1, head: { sha } },
  } };
  const failure = { headSha: sha, conclusion: 'failure', summary: 'PR #1: issue #9 lacks status:approved' };
  let state;
  try {
    assert.deepEqual(await ready, requests(['success', 'success'], ['Validation passed.', 'Validation passed.'])[1]);
    approvalState.approved = false;
    assert.deepEqual(await evaluateNativeIssuePolicy(github, context, 'approved'), failure);
    order.push('native current approval failure');
    assert.deepEqual(order, ['legacy approval snapshot ready', 'native current approval failure']);
    assert.deepEqual(nativeWrites, []);
  } finally {
    resumeLegacy();
    state = await legacy;
  }
  // This models the documented publication window, not GitHub's required-check selection.
  assert.deepEqual(order, ['legacy approval snapshot ready', 'native current approval failure', 'legacy approval publication']);
  assert.deepEqual(state.attempts, requests(['success', 'success'], ['Validation passed.', 'Validation passed.']));
  assert.deepEqual(state.stored, state.attempts.map((run) => ({ ...run, app: { id: 15368 } })));
  assert.deepEqual(state.failures, []);
  assert.deepEqual(state.reads, [['list', { ...repo, state: 'open', per_page: 100, page: 1 }],
    ['pull', { ...repo, pull_number: 1 }], ['issue', { ...repo, issue_number: 9 }],
    ['pull', { ...repo, pull_number: 1 }]]);
  assert.deepEqual(await evaluateNativeIssuePolicy(github, context, 'approved'), failure);
  const currentReads = [['pull', { ...repo, pull_number: 1 }], ['issue', { ...repo, issue_number: 9 }],
    ['pull', { ...repo, pull_number: 1 }]];
  assert.deepEqual(nativeReads, [...currentReads, ...currentReads]);
  assert.deepEqual(nativeWrites, []);
  assert.equal(approvalState.approved, false);
  assert.deepEqual(pull, { number: 1, state: 'open', body: 'Closes #9',
    head: { sha, repo: { full_name: 'untrusted/fork' } } });
});

test('composed refresh publishes assignment failure on the fetched head', async () => {
  const state = await execute({ assignees: [] });
  const expected = requests(['success', 'failure'], ['Validation passed.', 'PR #1: issue #9 has no assignee. Assign an owner before merge.']);
  assert.deepEqual(state.attempts, expected);
  assert.deepEqual(state.stored, expected.map((run) => ({ ...run, app: { id: 15368 } })));
  assert.deepEqual(state.failures, []);
  assert.deepEqual(state.reads.map(([method]) => method), ['list', 'pull', 'issue', 'pull']);
});
test('unrelated labels and unmatched references publish nothing', async () => {
  const irrelevant = await execute({ unrelated: true });
  assert.deepEqual(irrelevant, { reads: [], attempts: [], stored: [], failures: [] });
  const unmatched = await execute({ body: 'Closes #10' });
  assert.deepEqual(unmatched.reads, [['list', { ...repo, state: 'open', per_page: 100, page: 1 }], ['pull', { ...repo, pull_number: 1 }]]);
  assert.deepEqual([unmatched.attempts, unmatched.stored, unmatched.failures], [[], [], []]);
});

test('fork PR uses fetched head/body and requires approval for every reference', async () => {
  const state = await execute({ pr: true, action: 'reopened', body: 'Closes #9; Fixes #10', secondApproved: false });
  assert.deepEqual(state.attempts, requests(['success', 'failure'], ['Validation passed.', 'PR #1: issue #10 lacks status:approved']));
  assert.deepEqual(state.stored, state.attempts.map((run) => ({ ...run, app: { id: 15368 } })));
  assert.deepEqual(state.failures, []);
  assert.deepEqual(state.reads, [['pull', { ...repo, pull_number: 1 }], ['issue', { ...repo, issue_number: 9 }],
    ['issue', { ...repo, issue_number: 10 }], ['pull', { ...repo, pull_number: 1 }]]);
});

test('changed head publishes failures only for the initially fetched explicit target', async () => {
  const state = await execute({ changed: true });
  const message = 'PR #1: metadata changed; fresh reconciliation required';
  assert.deepEqual(state.attempts, requests(['failure', 'failure'], [message, message]));
  assert.deepEqual(state.stored, state.attempts.map((run) => ({ ...run, app: { id: 15368 } })));
  assert.deepEqual(state.failures, []);
});

for (const fault of ['metadata', 'issue', 'create', 'later', 'binding']) {
  test(`composed ${fault} failure never fabricates success or retries publication`, async () => {
    const state = await execute({ fault, pr: true, action: 'opened' });
    if (fault === 'issue') {
      assert.deepEqual(state.attempts, requests(['success', 'failure'], ['Validation passed.', 'PR #1: issue #9 unavailable (Issue unavailable)']));
      assert.deepEqual(state.failures, []);
    } else {
      const count = fault === 'metadata' ? 0 : fault === 'later' ? 2 : 1;
      assert.deepEqual(state.attempts, requests(['success', 'success'], ['Validation passed.', 'Validation passed.']).slice(0, count));
      const message = fault === 'metadata' ? 'Metadata denied' : fault === 'binding'
        ? 'Issue-check publisher blocker: returned check lacks GitHub Actions App15368/head/name/result binding'
        : 'Issue-check publisher blocker: Checks create failed (Checks denied); do not broaden credentials';
      assert.deepEqual(state.failures, [message]);
    }
    const persisted = fault === 'issue' ? 2 : ['later', 'binding'].includes(fault) ? 1 : 0;
    assert.equal(state.stored.length, persisted);
    assert.deepEqual(state.stored.map(({ app, ...run }) => run), state.attempts.slice(0, persisted));
  });
}
