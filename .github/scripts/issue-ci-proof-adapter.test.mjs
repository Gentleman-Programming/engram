import assert from 'node:assert/strict';
import test from 'node:test';
import { createGitHubProof } from './issue-ci-proof-adapter.mjs';
import { evaluateNativeIssuePolicy } from './issue-native-policy.mjs';

const head = 'a'.repeat(40), source = 'b'.repeat(40), repo = { owner: 'owner', repo: 'repo' };
function fixture(phase = 'native-read:approved') {
  const f = { time: 1000, trace: [], records: [], pullHead: head, nodes: null };
  f.cfg = { enabled: true, case: 'case-2030', issueNumber: 9, actorIds: [42], expiresAt: 100000,
    maxReads: 2, intervalMs: 1000, target: { repo: 'owner/repo', runId: 71, attempt: 2,
      phase, head, source: `${source}/${source}`, prNumber: 7, action: 'opened', checkout: source } };
  f.context = { repo, eventName: phase.startsWith('native') ? 'pull_request_target' : 'pull_request',
    payload: { action: 'opened', pull_request: { number: 7, head: { sha: head } } } };
  f.issue = { labels: [{ name: 'status:approved' }], assignees: [{ login: 'owner' }] };
  f.github = { rest: { pulls: { get: async () => {
    f.trace.push('pulls.get');
    return { data: { number: 7, state: 'open', body: 'Closes #9', head: { sha: f.pullHead } } };
  } }, issues: { get: async () => {
    f.trace.push('issues.get');
    f.response = { data: structuredClone(f.issue) };
    return f.response;
  } } }, graphql: async (query, variables) => {
    f.trace.push('graphql'); f.query = query; f.variables = variables;
    if (f.read) return f.read();
    return f.reply(f.nodes ?? [f.comment()]);
  } };
  f.reply = (nodes) => ({ repository: { issue: { comments: { nodes } } } });
  f.comment = (changes = {}) => {
    const { event, ...request } = f.records[0];
    return { body: JSON.stringify({ ...request, actorId: 999 }), lastEditedAt: null,
      author: { __typename: 'User', databaseId: 42 }, ...changes };
  };
  f.options = () => ({ github: f.github, context: f.context, raw: JSON.stringify(f.cfg), phase,
    checkout: source, runtime: { runId: 71, attempt: 2, workflow: source },
    emit: (record) => { f.records.push(record); f.checkpoint?.(); }, now: () => f.time,
    nonce: () => 'fresh-checkpoint-2030', wait: async (ms) => { f.trace.push(['wait', ms]); f.time += ms; } });
  return f;
}

for (const raw of [undefined, '', '{"enabled":false}']) test(`S4 disabled identity and zero calls: ${raw}`, async () => {
  const f = fixture(), proof = createGitHubProof({ ...f.options(), raw });
  assert.strictEqual(proof.client, f.github);
  const value = Object.freeze({ conclusion: 'failure' });
  assert.strictEqual(await proof.complete(value), value);
  proof.assert();
  assert.deepEqual(f.trace, []); assert.deepEqual(f.records, []);
});
for (const field of ['repo', 'prNumber', 'action', 'checkout', 'runId', 'attempt', 'phase', 'head', 'source']) test(`S6 unrelated target stays off: ${field}`, async () => {
  const f = fixture(); f.cfg.target[field] = 'other';
  const proof = createGitHubProof(f.options());
  assert.strictEqual(proof.client, f.github);
  assert.strictEqual(await proof.complete(false), false);
  assert.deepEqual(f.trace, []); assert.deepEqual(f.records, []);
});
test('S7 merge-group remains untouched, with no original-head substitution', async () => {
  const f = fixture('legacy-completion:approved'); f.context.eventName = 'merge_group';
  const proof = createGitHubProof(f.options());
  assert.strictEqual(proof.client, f.github);
  assert.strictEqual(await proof.complete(false), false); assert.deepEqual(f.trace, []);
});
for (const raw of ['PRIVATE invalid JSON', '{"enabled":true}', '{"enabled":"true"}']) test(`S6 malformed manifest rejects without reads: ${raw}`, () => {
  const f = fixture();
  assert.throws(() => createGitHubProof({ ...f.options(), raw }), /^Error: proof-adapter: configuration$/);
  assert.deepEqual(f.trace, []);
});
test('S8 actual native issue read remains in flight until authenticated release; original head guard survives', { timeout: 2000 }, async () => {
  const f = fixture(); let release, marked;
  const ready = new Promise((resolve) => { marked = resolve; }); f.checkpoint = marked;
  f.read = () => new Promise((resolve) => { release = () => resolve(f.reply([f.comment()])); });
  const proof = createGitHubProof(f.options());
  const pending = evaluateNativeIssuePolicy(proof.client, f.context, 'approved');
  // Drain the fake client's microtasks, then fail instead of awaiting an absent checkpoint.
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(f.records[0]?.event, 'checkpoint', 'actual read must reach its checkpoint');
  await ready; await Promise.resolve();
  assert.deepEqual(f.trace.slice(0, 2), ['pulls.get', 'issues.get']);
  assert.equal(f.trace.filter((x) => x === 'pulls.get').length, 1);
  f.pullHead = 'c'.repeat(40); f.issue.labels = []; release();
  const verdict = await pending; proof.assert();
  assert.equal(verdict.headSha, head); assert.equal(verdict.conclusion, 'failure');
  assert.match(verdict.summary, /metadata changed; fresh reconciliation required/);
  assert.equal(f.response.data.labels[0].name, 'status:approved');
  assert.equal(f.records[1].actorId, 42);
});
for (const policy of ['reference', 'approved']) test(`S8 native ${policy} preserves existing success and failure`, async () => {
  for (const labels of [[{ name: 'status:approved' }], []]) {
    const f = fixture(`native-read:${policy}`); f.issue.labels = labels;
    const proof = createGitHubProof(f.options());
    const verdict = await evaluateNativeIssuePolicy(proof.client, f.context, policy); proof.assert();
    assert.equal(verdict.conclusion, policy === 'approved' && !labels.length ? 'failure' : 'success');
    assert.equal(verdict.headSha, head); assert.equal(f.trace.filter((x) => x === 'graphql').length, 1);
    assert.deepEqual(f.variables, { ...repo, issue: 9 });
    assert.match(f.query, /comments\(last: 20\)/); assert.doesNotMatch(f.query, /mutation/);
  }
});
test('S6 swallowed read-control failure cannot turn into reference success', async () => {
  const f = fixture('native-read:reference'); f.read = async () => { throw new Error('PRIVATE transport'); };
  const proof = createGitHubProof(f.options());
  const verdict = await evaluateNativeIssuePolicy(proof.client, f.context, 'reference');
  assert.equal(verdict.conclusion, 'success');
  assert.throws(() => proof.assert(), /^Error: proof-control: transport$/);
  assert.deepEqual(f.records.map((x) => x.event), ['checkpoint']);
});
for (const change of [
  { author: { __typename: 'User', databaseId: 99 } },
  { author: { __typename: 'Bot', databaseId: 42 } },
  { author: null }, { lastEditedAt: '2026-10-22T00:00:00Z' },
  { lastEditedAt: undefined }, { body: 'PRIVATE not JSON' },
  { body: JSON.stringify({ actorId: 42 }) },
]) test(`S6 ignores forged/edited/incomplete release: ${JSON.stringify(change)}`, async () => {
  const f = fixture('legacy-completion:approved'); f.cfg.maxReads = 1;
  f.read = async () => f.reply([f.comment(change)]);
  const proof = createGitHubProof(f.options());
  await assert.rejects(proof.complete(Object.freeze(['original failure'])), /^Error: proof-control: exhausted$/);
  assert.deepEqual(f.records.map((x) => x.event), ['checkpoint']);
  assert.equal(f.trace.filter((x) => x === 'graphql').length, 1);
});
for (const policy of ['reference', 'approved']) test(`S8 legacy ${policy} preserves original result identity and one-use`, async () => {
  const f = fixture(`legacy-completion:${policy}`), proof = createGitHubProof(f.options());
  const result = Object.freeze(['original failure']);
  assert.strictEqual(await proof.complete(result), result); assert.strictEqual(proof.client, f.github);
  await assert.rejects(proof.complete(result), /^Error: proof-control: used$/);
  assert.deepEqual(result, ['original failure']); assert.equal(f.records[1].actorId, 42);
});
test('S6 polling cadence uses bounded reads without changing foundation limits', async () => {
  const f = fixture('legacy-completion:approved'); let count = 0;
  f.read = async () => f.reply(++count === 1 ? [] : [f.comment()]);
  const proof = createGitHubProof(f.options()); await proof.complete(false);
  assert.equal(count, 2); assert.deepEqual(f.trace.filter(Array.isArray), [['wait', 1000], ['wait', 29000]]);
});
test('S6 missing case rejects before any producer or witness reads', () => {
  const f = fixture(); delete f.cfg.case;
  assert.throws(() => createGitHubProof(f.options()), /^Error: proof-adapter: configuration$/);
  assert.deepEqual(f.trace, []);
});
test('S6 delayed polling never starts a read at the deadline', async () => {
  const f = fixture('legacy-completion:approved'); f.cfg.expiresAt = 2000;
  f.read = async () => f.reply([]);
  await assert.rejects(createGitHubProof(f.options()).complete(false), /^Error: proof-control: expired$/);
  assert.equal(f.trace.filter((x) => x === 'graphql').length, 1);
});
test('S6 prospective selector captures unpredictable run/source from trusted runtime', async () => {
  const f = fixture('legacy-completion:approved');
  for (const key of ['runId', 'attempt', 'source']) delete f.cfg.target[key];
  assert.strictEqual(await createGitHubProof(f.options()).complete(false), false);
  assert.equal(f.records[0]?.runId, 71); assert.equal(f.records[0]?.attempt, 2);
  assert.equal(f.records[0]?.source, `${source}/${source}`);
});
test('S6 malformed response fails closed and never exposes response data', async () => {
  const f = fixture('legacy-completion:approved'); f.read = async () => ({ PRIVATE: 'SECRET' });
  await assert.rejects(createGitHubProof(f.options()).complete(true), /^Error: proof-control: transport$/);
});
