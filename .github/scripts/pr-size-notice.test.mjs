import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
const { publishSizeNotice } = await import('./pr-size-notice.mjs').catch((error) => ({
  publishSizeNotice: async () => { throw error; },
}));

async function notice(pull, options = {}) {
  const before = structuredClone(pull);
  const effects = { requests: [], warnings: [], infos: [], headings: [], summaries: [], writes: 0 };
  const github = { rest: { pulls: { get: async (request) => {
    effects.requests.push(request);
    if (options.apiError) throw new Error('API unavailable');
    return { data: pull };
  } } } };
  const core = {
    warning: (message) => effects.warnings.push(message),
    info: (message) => effects.infos.push(message),
    summary: {
      addHeading(value) { effects.headings.push(value); return this; },
      addRaw(value) { effects.summaries.push(value); return this; },
      async write() { effects.writes++; if (options.summaryError) throw new Error('Write unavailable'); },
    },
  };
  // Deliberately stale event metadata must not determine the notice.
  const context = { repo: { owner: 'owner', repo: 'repo' }, issue: { number: 12 },
    payload: { pull_request: { additions: 999, deletions: 999, labels: [{ name: 'size:exception' }] } } };
  await publishSizeNotice({ github, context, core });
  assert.deepEqual(pull, before, 'read-only metadata handling');
  assert.deepEqual(effects.requests, [{ owner: 'owner', repo: 'repo', pull_number: 12 }]);
  assert.deepEqual(effects.headings, ['PR size notice']);
  assert.equal(effects.writes, 1);
  return effects;
}

const pull = (additions, deletions, labels = []) => ({ additions, deletions, labels });
const within = 'Within review budget: 400 changed lines (budget: 400).';
const oversized = 'Oversized pull request: 401 changed lines (budget: 400). Consider splitting the change or requesting a maintainer exception.';
const exception = 'Maintainer exception: 401 changed lines (budget: 400; size:exception present).';
const unavailable = 'Size unavailable: current pull-request counts or labels could not be determined.';

function output(effects, message, warning = false) {
  assert.deepEqual(effects.summaries, [message]);
  assert.deepEqual(effects.warnings, warning ? [message] : []);
  assert.deepEqual(effects.infos, warning ? [] : [message]);
}

test('counts all GitHub additions and deletions: 400 is within budget, 401 warns', async () => {
  output(await notice(pull(200, 200)), within);
  output(await notice(pull(200, 201)), oversized, true);
  output(await notice(pull(0, 401)), oversized, true);
  output(await notice(pull(401, 0)), oversized, true);
});

test('respects label presence without granting or claiming verified labeler authority', async () => {
  output(await notice(pull(200, 201, [{ name: 'size:exception' }])), exception);
  output(await notice(pull(200, 201, [{ name: 'type:feature' }])), oversized, true);
});

test('refreshes the notice when the diff shrinks and the exception changes', async () => {
  output(await notice(pull(200, 201)), oversized, true);
  output(await notice(pull(200, 200)), within);
  output(await notice(pull(200, 201, [{ name: 'size:exception' }])), exception);
  output(await notice(pull(200, 201)), oversized, true);
});

test('unknown and invalid metadata never produces a within-budget or oversized result', async () => {
  for (const value of [undefined, null, -1, '200', 1.5, Number.MAX_SAFE_INTEGER]) {
    output(await notice(pull(value, 200)), unavailable, true);
    output(await notice(pull(200, value)), unavailable, true);
  }
  for (const value of [undefined, null, {}, [{ name: null }]]) {
    output(await notice({ additions: 200, deletions: 200, labels: value }), unavailable, true);
  }
  output(await notice(undefined), unavailable, true);
  output(await notice(pull(200, 200), { apiError: true }), unavailable, true);
});

test('summary publication errors stay informative rather than failing the job', async () => {
  const effects = await notice(pull(200, 200), { summaryError: true });
  assert.deepEqual(effects.summaries, [within]);
  assert.deepEqual(effects.infos, [within]);
  assert.deepEqual(effects.warnings, ['PR size notice summary could not be published.']);
});

test('checks out the executing workflow revision when the PR base lacks the script', () => {
  const workflow = fs.readFileSync('.github/workflows/pr-size-notice.yml', 'utf8');
  const workflowSha = 'a'.repeat(40);
  const oldBaseSha = 'b'.repeat(40);
  const forkHeadSha = 'c'.repeat(40);
  const contextRefs = {
    'github.workflow_sha': workflowSha,
    'github.event.pull_request.base.sha': oldBaseSha,
    'github.event.pull_request.head.sha': forkHeadSha,
  };
  const scriptsByRevision = new Map([
    [workflowSha, new Set(['.github/scripts/pr-size-notice.mjs'])],
    [oldBaseSha, new Set()],
    [forkHeadSha, new Set(['.github/scripts/pr-size-notice.mjs'])],
  ]);
  const expression = workflow.match(/^          ref: \$\{\{ ([\w.]+) \}\}$/m)?.[1];
  const checkoutSha = contextRefs[expression];
  assert.equal(checkoutSha, workflowSha, 'checkout must match the trusted executing workflow, not the historical PR base or fork head');
  assert.ok(scriptsByRevision.get(checkoutSha)?.has('.github/scripts/pr-size-notice.mjs'));
});

test('workflow is trusted-base, SHA-pinned, read-only, non-blocking and comment-free', () => {
  const workflow = fs.readFileSync('.github/workflows/pr-size-notice.yml', 'utf8');
  assert.match(workflow, /^  pull_request_target:/m);
  assert.doesNotMatch(workflow, /merge_group|pull_request:|: write|setFailed|npm |run:/);
  assert.match(workflow, /^  contents: read$/m);
  assert.match(workflow, /^  pull-requests: read$/m);
  assert.match(workflow, /name: PR Size Notice/);
  assert.match(workflow, /cancel-in-progress: true/);
  assert.match(workflow, /ref: \$\{\{ github.workflow_sha \}\}/);
  assert.doesNotMatch(workflow, /github.event.pull_request.base.sha/);
  assert.match(workflow, /persist-credentials: false/);
  assert.doesNotMatch(workflow, /pull_request.head|createComment|updateComment|checks\./);
  assert.deepEqual([...workflow.matchAll(/uses: ([^\s]+)@([a-f0-9]{40})/g)].map((match) => match[1]), ['actions/checkout', 'actions/github-script']);
  assert.match(workflow, /await publishSizeNotice\(\{ github, context, core \}\)/);
});

test('refreshes on base-branch retargeting while retaining all previous PR events', () => {
  const workflow = fs.readFileSync('.github/workflows/pr-size-notice.yml', 'utf8');
  const events = workflow.match(/^    types: \[([^\]]+)\]/m)?.[1].split(',').map((event) => event.trim());
  assert.deepEqual(events, ['opened', 'edited', 'synchronize', 'reopened', 'labeled', 'unlabeled']);
});

test('CI includes the new suite while preserving all previous policy helper suites', () => {
  const ci = fs.readFileSync('.github/workflows/ci.yml', 'utf8');
  assert.match(ci, /node --test .github\/scripts\/merge-queue.test.mjs .github\/scripts\/label-policy.test.mjs .github\/scripts\/transient-artifacts.test.mjs .github\/scripts\/pr-size-notice.test.mjs/);
});
