import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';

const repo = { owner: 'upstream', repo: 'project' };
const head = 'a'.repeat(40), source = 'b'.repeat(40), trusted = 'c'.repeat(40);
const names = ['Check Issue Reference', 'Check Issue Has status:approved'];
const paths = ['.github/workflows/issue-policy.yml', '.github/scripts/issue-native-policy.mjs', '.github/scripts/issue-validation.mjs'];
const pull = { number: 7, state: 'open', body: 'Closes #8', head: { sha: head, ref: 'topic', repo: { id: 17, full_name: 'fork/project' } }, base: { ref: 'main', repo: { id: 18, full_name: 'upstream/project' } } };
const clone = (value) => structuredClone(value);
const eventPrefix = 'Original issue event v1 ';
const eventRecord = (run, original = pull, action = 'opened') => ['pull_request_target', action,
  original.base.repo.id, run.id, run.run_attempt, original.number, original.head.repo.id,
  original.head.sha, original.head.ref, original.base.repo.id, original.base.ref];
const eventStep = (run, original = pull, action) => ({ name: eventPrefix + JSON.stringify(eventRecord(run, original, action)), status: 'completed', conclusion: 'success' });
function fixture(change = () => {}) {
  const run = { id: 90, workflow_id: 12, path: paths[0], event: 'pull_request_target', head_sha: head, head_branch: 'topic', repository: { full_name: 'upstream/project' }, head_repository: { full_name: 'fork/project' }, run_attempt: 2, status: 'completed', created_at: new Date().toISOString(), pull_requests: [{ number: pull.number, head: clone(pull.head), base: clone(pull.base) }] };
  const jobs = names.map((name, i) => ({ id: i + 1, name, head_sha: head, run_id: 90, status: 'completed', steps: [{ name: `Load trusted issue policy (${source})`, status: 'completed', conclusion: 'success' }, eventStep(run)] }));
  const state = { run, jobs, current: clone(pull), final: clone(pull), compare: { status: 'ahead', merge_base_commit: { sha: source } }, workflow: { id: 12, path: paths[0], state: 'active' }, calls: [], reads: 0 };
  change(state);
  const github = { request: async (route, params) => {
    state.calls.push({ route, params });
    if (state.reject === route) throw Object.assign(new Error('API denied'), { status: 403 });
    switch (route) {
      case 'GET /repos/{owner}/{repo}/pulls/{pull_number}': return { data: clone(++state.reads === 1 ? state.current : state.final) };
      case 'GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}': return { data: state.workflow };
      case 'GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs': return { data: { workflow_runs: state.noRuns ? [] : (state.runs ?? [state.run]), total_count: state.count ?? (state.noRuns ? 0 : (state.runs?.length ?? 1)) } };
      case 'GET /repos/{owner}/{repo}/actions/runs/{run_id}': return { data: state.freshRun ?? state.run };
      case 'GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs': return { data: { jobs: state.jobs, total_count: state.jobs.length } };
      case 'GET /repos/{owner}/{repo}/compare/{basehead}': return { data: state.compare };
      case 'GET /repos/{owner}/{repo}/contents/{path}': {
        const text = state.stale && params.ref === source ? 'old policy' : 'trusted policy';
        const buffer = Buffer.from(text);
        return { data: { type: 'file', encoding: 'base64', content: buffer.toString('base64'), size: buffer.length, sha: state.badBlob ? head : createHash('sha1').update(`blob ${buffer.length}\0`).update(buffer).digest('hex') } };
      }
      case 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun': return { status: state.postStatus ?? 201 };
      default: throw new Error(`Unexpected request ${route}`);
    }
  } };
  return { github, state };
}
const load = async () => (await import('./issue-refresh-router.mjs')).rerunNativeIssuePolicy;
const writes = (state) => state.calls.filter(({ route }) => !route.startsWith('GET '));

test('fresh fork head with unambiguous run PR identity requests exactly one metadata rerun', async () => {
  const { github, state } = fixture();
  assert.deepEqual(await (await load())(github, repo, pull, trusted), { status: 'requested', runId: 90, headSha: head, sourceSha: source, trustedSha: trusted, attempt: 2 });
  assert.deepEqual(writes(state), [{ route: 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun', params: { ...repo, run_id: 90 } }]);
  assert.equal(state.reads, 2);
  const expected = [
    ['GET /repos/{owner}/{repo}/pulls/{pull_number}', { ...repo, pull_number: 7 }],
    ['GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}', { ...repo, workflow_id: 'issue-policy.yml' }],
    ['GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs', { ...repo, workflow_id: 'issue-policy.yml', event: 'pull_request_target', branch: 'topic', head_sha: head, per_page: 100, page: 1 }],
    ['GET /repos/{owner}/{repo}/actions/runs/{run_id}', { ...repo, run_id: 90 }],
    ['GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs', { ...repo, run_id: 90, attempt_number: 2, per_page: 100, page: 1 }],
    ['GET /repos/{owner}/{repo}/compare/{basehead}', { ...repo, basehead: `${source}...${trusted}` }],
    ...paths.flatMap((path) => [source, trusted].map((ref) => ['GET /repos/{owner}/{repo}/contents/{path}', { ...repo, path, ref }])),
    ['GET /repos/{owner}/{repo}/pulls/{pull_number}', { ...repo, pull_number: 7 }],
  ];
  assert.deepEqual(state.calls.filter(({ route }) => route.startsWith('GET ')).map(({ route, params }) => [route, params]), expected);
});

test('same fork/head/SHA PRs targeting different bases never rerun the other PR or fall back', async () => {
  const { github, state } = fixture((s) => {
    const intended = clone(s.run);
    s.run.id = 91;
    s.run.pull_requests[0].number = 8;
    s.run.pull_requests[0].base.ref = 'release';
    s.runs = [intended, s.run];
    s.jobs.forEach((job) => { job.run_id = 91; });
  });
  const before = clone(state.runs);
  await assert.rejects((await load())(github, repo, pull, trusted), /^Error: Issue refresh blocked: native run PR\/event binding unavailable$/);
  assert.deepEqual(writes(state), []);
  assert.deepEqual(state.runs, before);
  assert.equal(state.reads, 1);
});

const guards = {
  'missing head repo id': (s) => { delete s.current.head.repo.id; },
  'final base repo id drift': (s) => { s.final.base.repo.id = 19; },
  'missing run PR association': (s) => { delete s.run.pull_requests; },
  'malformed run PR association': (s) => { s.run.pull_requests = {}; },
  'ambiguous run PR association': (s) => { s.run.pull_requests.push(clone(s.run.pull_requests[0])); },
  'other run PR number': (s) => { s.run.pull_requests[0].number = 8; },
  'other run PR base branch': (s) => { s.run.pull_requests[0].base.ref = 'release'; },
  'other run PR base repository': (s) => { s.run.pull_requests[0].base.repo.id = 19; },
  'other run PR head repository': (s) => { s.run.pull_requests[0].head.repo.id = 19; },
  'other run PR head ref': (s) => { s.run.pull_requests[0].head.ref = 'other'; },
  'other run PR head SHA': (s) => { s.run.pull_requests[0].head.sha = trusted; },
  'fresh run PR identity drift': (s) => { s.freshRun = clone(s.run); s.freshRun.pull_requests[0].number = 8; },
  'initial body drift': (s) => { s.current.body = 'Closes #9'; },
  'final head drift': (s) => { s.final.head.sha = trusted; },
  'closed final target': (s) => { s.final.state = 'closed'; },
  'missing head repo': (s) => { delete s.current.head.repo; },
  'wrong PR number': (s) => { s.current.number = 9; },
  'base branch drift': (s) => { s.final.base.ref = 'release'; },
  'head ref drift': (s) => { s.final.head.ref = 'other'; },
  'fork drift': (s) => { s.final.head.repo.full_name = 'evil/project'; },
  'base repo drift': (s) => { s.final.base.repo.full_name = 'evil/project'; },
  'incomplete run listing': (s) => { s.count = 2; },
  'unbound protected blob': (s) => { s.badBlob = true; },
  'source merge base mismatch': (s) => { s.compare.merge_base_commit.sha = trusted; },
  'no native run': (s) => { s.noRuns = true; },
  'latest run read drift': (s) => { s.freshRun = { ...s.run, head_sha: trusted }; },
  'workflow unavailable': (s) => { s.workflow.state = 'disabled_manually'; },
  'duplicate job names': (s) => { s.jobs[1].name = s.jobs[0].name; },
  'missing created timestamp': (s) => { delete s.run.created_at; },
  'running attempt': (s) => { s.run.status = 'in_progress'; },
  'wrong workflow path': (s) => { s.run.path = '.github/workflows/pr-check.yml'; },
  'wrong workflow id': (s) => { s.run.workflow_id = 99; },
  'wrong event': (s) => { s.run.event = 'pull_request'; },
  'wrong run head': (s) => { s.run.head_sha = trusted; },
  'wrong run ref': (s) => { s.run.head_branch = 'other'; },
  'wrong run repository': (s) => { s.run.repository.full_name = 'fork/project'; },
  'wrong run fork': (s) => { s.run.head_repository.full_name = 'other/project'; },
  'missing attempt': (s) => { delete s.run.run_attempt; },
  '50 attempts': (s) => { s.run.run_attempt = 50; },
  'older than 30 days': (s) => { s.run.created_at = new Date(Date.now() - 31 * 86400000).toISOString(); },
  'broad job shape': (s) => { s.jobs.push({ ...s.jobs[0], name: 'Unit Tests' }); },
  'wrong job head': (s) => { s.jobs[0].head_sha = trusted; },
  'wrong job run': (s) => { s.jobs[0].run_id = 91; },
  'failed checkout': (s) => { s.jobs[0].steps[0].conclusion = 'failure'; },
  'missing checkout': (s) => { s.jobs[0].steps = []; },
  'unresolved checkout': (s) => { s.jobs[0].steps[0].name = 'Load trusted issue policy (${{ github.workflow_sha }})'; },
  'different source markers': (s) => { s.jobs[0].steps[0].name = `Load trusted issue policy (${trusted})`; },
  'source not ancestor': (s) => { s.compare.status = 'diverged'; },
  'protected content drift': (s) => { s.stale = true; },
  'token/API denial': (s) => { s.reject = 'GET /repos/{owner}/{repo}/actions/runs/{run_id}'; },
};
for (const [name, change] of Object.entries(guards)) test(`${name} blocks with zero writes`, async () => {
  const { github, state } = fixture(change);
  await assert.rejects((await load())(github, repo, pull, trusted), /Issue refresh blocked:/);
  assert.deepEqual(writes(state), []);
});

test('49 attempts and unchanged policy across unrelated main revisions remain eligible', async () => {
  const { github, state } = fixture((s) => {
    s.run.run_attempt = 49;
    s.jobs.forEach((job) => { job.conclusion = 'failure'; job.steps[1] = eventStep(s.run); });
  });
  const result = await (await load())(github, repo, pull, trusted);
  assert.equal(result.attempt, 49);
  assert.equal(writes(state).length, 1);
});

for (const action of ['opened', 'edited', 'labeled', 'unlabeled', 'synchronize', 'reopened']) test(`trusted original ${action} event admits empty fork associations with one metadata write`, async () => {
  const { github, state } = fixture((s) => {
    s.run.pull_requests = [];
    s.jobs.forEach((job) => { job.steps[1] = eventStep(s.run, pull, action); job.conclusion = 'failure'; });
  });
  const before = clone([state.run, state.jobs, state.current, state.final]);
  assert.deepEqual(await (await load())(github, repo, pull, trusted),
    { status: 'requested', runId: 90, headSha: head, sourceSha: source, trustedSha: trusted, attempt: 2 });
  assert.deepEqual(writes(state), [{ route: 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun', params: { ...repo, run_id: 90 } }]);
  assert.equal(state.reads, 2);
  assert.deepEqual([state.run, state.jobs, state.current, state.final], before);
});

const identityGuards = {
  'legacy run without either marker': (s) => { s.jobs.forEach((job) => job.steps.pop()); },
  'one job missing marker': (s) => { s.jobs[0].steps.pop(); },
  'duplicate marker': (s) => { s.jobs[0].steps.push(clone(s.jobs[0].steps[1])); },
  'unknown marker version': (s) => { s.jobs[0].steps[1].name = 'Original issue event v2 []'; },
  'malformed marker JSON': (s) => { s.jobs[0].steps[1].name = eventPrefix + '{'; },
  'marker object instead of tuple': (s) => { s.jobs[0].steps[1].name = eventPrefix + '{}'; },
  'unsuccessful marker': (s) => { s.jobs[0].steps[1].conclusion = 'failure'; },
  'incomplete marker': (s) => { s.jobs[0].steps[1].status = 'in_progress'; },
  'jobs disagree on original action': (s) => { s.jobs[0].steps[1] = eventStep(s.run, pull, 'edited'); },
};
for (const [index, value] of [[0, 'push'], [1, 'closed'], [2, 19], [3, 91], [4, 1],
  [5, 8], [6, 19], [7, trusted], [8, 'other'], [9, 19], [10, 'release'], [11, 'extra'], [3, '90']]) {
  identityGuards[`marker tuple field ${index} mismatch ${JSON.stringify(value)}`] = (s) => {
    const record = eventRecord(s.run); record[index] = value;
    s.jobs[0].steps[1].name = eventPrefix + JSON.stringify(record);
  };
}
for (const [name, change] of Object.entries(identityGuards)) test(`${name} blocks with or without API PR association`, async () => {
  for (const empty of [false, true]) {
    const { github, state } = fixture((s) => { if (empty) s.run.pull_requests = []; change(s); });
    const before = clone([state.run, state.jobs, state.current, state.final]);
    await assert.rejects((await load())(github, repo, pull, trusted),
      /^Error: Issue refresh blocked: native original PR\/event binding unavailable$/);
    assert.deepEqual(writes(state), []);
    assert.equal(state.reads, 1);
    assert.deepEqual([state.run, state.jobs, state.current, state.final], before);
  }
});

test('same fork/head/base with another original PR never falls back to an older matching event', async () => {
  const { github, state } = fixture((s) => {
    const old = clone(s.run); s.run.id = 91; s.run.pull_requests = [];
    const other = { ...pull, number: 8 };
    s.jobs.forEach((job) => { job.run_id = 91; job.steps[1] = eventStep(s.run, other); });
    s.runs = [old, s.run];
  });
  await assert.rejects((await load())(github, repo, pull, trusted), /native original PR\/event binding unavailable/);
  assert.deepEqual(writes(state), []);
  assert.deepEqual(state.calls.filter(({ route }) => route.endsWith('/runs/{run_id}')).map(({ params }) => params.run_id), [91]);
});

test('JSON-escaped refs are compared as data, never split on punctuation', async () => {
  const target = clone(pull); target.head.ref = 'topic|"quoted"';
  const { github, state } = fixture((s) => {
    s.current = clone(target); s.final = clone(target); s.run.head_branch = target.head.ref;
    s.run.pull_requests = []; s.jobs.forEach((job) => { job.steps[1] = eventStep(s.run, target); });
  });
  assert.equal((await (await load())(github, repo, target, trusted)).status, 'requested');
  assert.equal(writes(state).length, 1);
});

test('malformed caller bindings block before any request', async () => {
  const { github, state } = fixture();
  await assert.rejects((await load())(github, repo, pull, 'main'), /trusted repository\/SHA/);
  assert.deepEqual(state.calls, []);
});

test('non-201 acknowledgement cannot claim a requested rerun', async () => {
  const { github, state } = fixture((s) => { s.postStatus = 202; });
  await assert.rejects((await load())(github, repo, pull, trusted), /not acknowledged/);
  assert.equal(writes(state).length, 1);
});

for (const route of [
  'GET /repos/{owner}/{repo}/pulls/{pull_number}',
  'GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}',
  'GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs',
  'GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs',
  'GET /repos/{owner}/{repo}/compare/{basehead}',
  'GET /repos/{owner}/{repo}/contents/{path}',
]) test(`read API failure at ${route} blocks without retry or writes`, async () => {
  const { github, state } = fixture((s) => { s.reject = route; });
  await assert.rejects((await load())(github, repo, pull, trusted), /Issue refresh blocked: API denied/);
  assert.equal(state.calls.filter((call) => call.route === route).length, 1);
  assert.deepEqual(writes(state), []);
});

test('rerun API rejection is explicit and never retried', async () => {
  const { github, state } = fixture((s) => { s.reject = 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun'; });
  await assert.rejects((await load())(github, repo, pull, trusted), /API denied/);
  assert.equal(writes(state).length, 1);
});
