import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';

const repo = { owner: 'upstream', repo: 'project' };
const head = 'a'.repeat(40), source = 'b'.repeat(40), trusted = 'c'.repeat(40);
const context = (action = 'labeled') => ({ repo, eventName: 'issues', payload: { action, label: { name: 'status:approved' }, issue: { number: 8 } } });
const pull = (number = 7, body = 'Closes #8; fixes #9') => ({ number, body, state: 'open', head: { sha: head, ref: 'topic', repo: { id: 17, full_name: 'fork/project' } }, base: { ref: 'main', repo: { id: 18, full_name: 'upstream/project' } } });
const clone = (value) => structuredClone(value);
const load = async () => (await import('./issue-refresh-dispatch.mjs')).dispatchIssueApprovalRefresh;
function fixture() {
  const state = { pages: [[pull()]], pulls: new Map([[7, pull()]]), calls: [], reads: new Map() };
  const github = { request: async (route, params) => {
    state.calls.push({ route, params });
    if (state.reject === route) throw new Error('API denied');
    if (route === 'GET /repos/{owner}/{repo}/pulls') return { data: state.pages[params.page - 1] ?? [] };
    if (route === 'GET /repos/{owner}/{repo}/pulls/{pull_number}') {
      const count = (state.reads.get(params.pull_number) ?? 0) + 1;
      state.reads.set(params.pull_number, count);
      return { data: clone(count > 1 && state.drift ? state.drift : state.pulls.get(params.pull_number)) };
    }
    const target = state.pulls.get(state.activeNumber ?? 7);
    const run = { id: 90, workflow_id: 12, path: '.github/workflows/issue-policy.yml', event: 'pull_request_target', head_sha: head, head_branch: 'topic', repository: { full_name: 'upstream/project' }, head_repository: { full_name: 'fork/project' }, run_attempt: 2, status: 'completed', created_at: new Date().toISOString(), pull_requests: [{ number: target.number, head: target.head, base: target.base }] };
    switch (route) {
      case 'GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}': return { data: { id: 12, path: run.path, state: 'active' } };
      case 'GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs': return { data: { total_count: state.noRuns ? 0 : 1, workflow_runs: state.noRuns ? [] : [run] } };
      case 'GET /repos/{owner}/{repo}/actions/runs/{run_id}': return { data: run };
      case 'GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs': return { data: { total_count: 2, jobs: ['Check Issue Reference', 'Check Issue Has status:approved'].map((name) => ({ name, run_id: 90, head_sha: head, status: 'completed', steps: [{ name: `Load trusted issue policy (${source})`, status: 'completed', conclusion: 'success' }] })) } };
      case 'GET /repos/{owner}/{repo}/compare/{basehead}': return { data: { status: 'ahead', merge_base_commit: { sha: source } } };
      case 'GET /repos/{owner}/{repo}/contents/{path}': {
        const bytes = Buffer.from('trusted policy');
        return { data: { type: 'file', encoding: 'base64', content: bytes.toString('base64'), size: bytes.length, sha: createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex') } };
      }
      case 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun': return { status: state.postStatus ?? 201 };
      default: throw new Error(`Unexpected request ${route}`);
    }
  } };
  return { github, state };
}
const writes = (state) => state.calls.filter(({ route }) => !route.startsWith('GET '));
for (const action of ['labeled', 'unlabeled']) test(`${action} requests the real admitted helper once for multiple closing refs`, async () => {
  const { github, state } = fixture();
  assert.deepEqual(await (await load())(github, context(action), trusted), [{ status: 'requested', runId: 90, headSha: head, sourceSha: source, trustedSha: trusted, attempt: 2 }]);
  assert.deepEqual(writes(state), [{ route: 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun', params: { ...repo, run_id: 90 } }]);
  assert.deepEqual(state.calls.slice(0, 3), [
    { route: 'GET /repos/{owner}/{repo}/pulls', params: { ...repo, state: 'open', per_page: 100, page: 1 } },
    { route: 'GET /repos/{owner}/{repo}/pulls/{pull_number}', params: { ...repo, pull_number: 7 } },
    { route: 'GET /repos/{owner}/{repo}/pulls/{pull_number}', params: { ...repo, pull_number: 7 } },
  ]);
  assert.equal(state.reads.get(7), 3);
});
for (const change of [
  (c) => { c.eventName = 'pull_request_target'; },
  (c) => { c.payload.action = 'opened'; },
  (c) => { c.payload.label.name = 'type:bug'; },
  (c) => { c.payload.issue.pull_request = {}; },
  (c) => { c.payload.issue.number = 0; },
]) test('ineligible events return [] without any request', async () => {
  const { github, state } = fixture(), c = context(); change(c);
  assert.deepEqual(await (await load())(github, c, trusted), []);
  assert.deepEqual(state.calls, []);
});
test('complete pagination and all current reads precede dispatch; listing bodies are not proof', async () => {
  const { github, state } = fixture();
  state.pages = [Array.from({ length: 100 }, (_, i) => pull(i + 1)), [pull(101, 'unrelated listing')]];
  state.pulls = new Map(Array.from({ length: 101 }, (_, i) => [i + 1, pull(i + 1, 'No closing reference')]));
  state.pulls.set(101, pull(101)); state.activeNumber = 101;
  const results = await (await load())(github, context(), trusted);
  assert.equal(results.length, 1);
  assert.deepEqual(state.calls.slice(0, 2).map((c) => c.params.page), [1, 2]);
  const workflowIndex = state.calls.findIndex((c) => c.route.includes('/actions/'));
  assert.equal(workflowIndex, 104);
  assert.equal(writes(state).length, 1);
  assert.equal(state.reads.get(101), 3);
  assert.equal(state.reads.get(1), 1);
});
test('current closed/unrelated/body drift targets cause no policy writes', async () => {
  const { github, state } = fixture();
  state.pages = [[pull(7), pull(10), pull(11)]];
  state.pulls.set(7, pull(7, 'Closes #99'));
  state.pulls.set(10, { ...pull(10), state: 'closed' });
  state.pulls.set(11, pull(11, null));
  assert.deepEqual(await (await load())(github, context(), trusted), []);
  assert.deepEqual(writes(state), []);
  assert.equal(state.calls.length, 4);
});
for (const [name, change] of [
  ['listing API', (s) => { s.reject = 'GET /repos/{owner}/{repo}/pulls'; }],
  ['current PR API', (s) => { s.reject = 'GET /repos/{owner}/{repo}/pulls/{pull_number}'; }],
  ['malformed listing', (s) => { s.pages = [{}]; }],
  ['invalid number', (s) => { s.pages = [[pull(0)]]; }],
  ['duplicate number', (s) => { s.pages = [[pull(), pull()]]; }],
  ['wrong current number', (s) => { s.pulls.set(7, pull(9)); }],
  ['malformed current body', (s) => { s.pulls.get(7).body = {}; }],
  ['missing eligible run', (s) => { s.noRuns = true; }],
  ['helper body drift', (s) => { s.drift = pull(7, 'Closes #99'); }],
]) test(`${name} fails explicitly with zero writes`, async () => {
  const { github, state } = fixture(); change(state);
  await assert.rejects((await load())(github, context(), trusted), /Issue refresh/);
  assert.deepEqual(writes(state), []);
});
test('later discovery failure prevents dispatch of previously affected PR', async () => {
  const { github, state } = fixture(); state.pages = [[pull(), pull(10)]];
  await assert.rejects((await load())(github, context(), trusted), /metadata/);
  assert.deepEqual(writes(state), []);
});
test('each affected target is attempted despite rejection; partial results are explicit, never retried', async () => {
  const { github, state } = fixture();
  state.pages = [[pull(), pull(10)]]; state.pulls.set(10, pull(10));
  // PR 10 has no matching original event identity in the real helper fixture.
  await assert.rejects((await load())(github, context(), trusted), (error) => {
    assert.equal(error.results.length, 1);
    assert.deepEqual(error.failures.map((f) => f.pullNumber), [10]);
    assert.match(error.failures[0].error.message, /PR\/event binding/);
    return true;
  });
  assert.equal(writes(state).length, 1);
  assert.equal(state.reads.get(10), 2);
});
test('a rejected first target does not suppress a later admitted target', async () => {
  const { github, state } = fixture();
  state.pages = [[pull(), pull(10)]]; state.pulls.set(10, pull(10)); state.activeNumber = 10;
  await assert.rejects((await load())(github, context(), trusted), (error) => {
    assert.equal(error.results.length, 1);
    assert.deepEqual(error.failures.map((f) => f.pullNumber), [7]);
    return true;
  });
  assert.deepEqual(writes(state), [{ route: 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun', params: { ...repo, run_id: 90 } }]);
  assert.equal(state.reads.get(7), 2);
  assert.equal(state.reads.get(10), 3);
});
test('repeated pagination fails before any current read or write', async () => {
  const { github, state } = fixture();
  state.pages = [Array.from({ length: 100 }, (_, i) => pull(i + 1)), [pull(1)]];
  await assert.rejects((await load())(github, context(), trusted), /repeated/);
  assert.equal(state.calls.length, 2);
  assert.deepEqual(writes(state), []);
});
for (const failure of ['denied', 'unacknowledged']) test(`POST ${failure} remains explicit without fallback`, async () => {
  const { github, state } = fixture();
  if (failure === 'denied') state.reject = 'POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun';
  else state.postStatus = 202;
  await assert.rejects((await load())(github, context(), trusted), (error) => error.results.length === 0 && error.failures.length === 1);
  assert.equal(writes(state).length, 1);
});
