import test from 'node:test';
import assert from 'node:assert/strict';
import { holdCI } from './issue-ci-proof-ci.mjs';

const sha = 'a'.repeat(40);
const base = 'b'.repeat(40);
const workflow = 'c'.repeat(40);
function fixture(event = 'pull_request') {
  let calls = 0, checkpoint;
  const identity = { repo: 'owner/repo', event, action: event === 'merge_group' ? 'checks_requested' : 'synchronize',
    job: 'unit-tests', head: sha, checkout: base, workflow, runId: 12, attempt: 2,
    prHead: event === 'pull_request' ? 'd'.repeat(40) : null };
  const cfg = { enabled: true, target: identity, case: 'independent', issueNumber: 42,
    actorIds: [7], expiresAt: 1500, maxReads: 1, intervalMs: 1 };
  const node = () => ({ body: JSON.stringify({ ...checkpoint, actorId: 999 }), lastEditedAt: null,
    author: { __typename: 'User', databaseId: 7 } });
  const args = { raw: JSON.stringify(cfg), identity, result: Object.freeze({ original: true }),
    now: () => 1000, nonce: () => 'fresh_checkpoint_123', emit: (r) => { checkpoint = r; },
    github: { graphql: async (query, variables) => {
      calls++;
      assert.equal(checkpoint.runId, identity.runId);
      assert.equal(checkpoint.attempt, identity.attempt);
      assert.equal(checkpoint.head, identity.head);
      assert.equal(checkpoint.source, `${identity.workflow}/${identity.checkout}`);
      assert.equal(checkpoint.phase, `ci:unit-tests:${event}:${identity.action}:${identity.prHead ?? 'group'}`);
      assert.match(query, /comments\(last: 20\)/);
      assert.deepEqual(variables, { owner: 'owner', repo: 'repo', issue: 42 });
      return { repository: { issue: { comments: { nodes: [args.record ? args.record(node()) : node()] } } } };
    } } };
  return { args, cfg, calls: () => calls, update: () => { args.raw = JSON.stringify(cfg); } };
}

test('OFF returns original identity without API calls', async () => {
  for (const raw of [undefined, '', '{"enabled":false}']) {
    const f = fixture(); f.args.raw = raw;
    assert.equal(await holdCI(f.args), f.args.result);
    assert.equal(f.calls(), 0);
  }
});

for (const event of ['pull_request', 'merge_group']) {
  test(`${event} binds actual runtime and authenticates server author`, async () => {
    const f = fixture(event);
    assert.equal(await holdCI(f.args), f.args.result);
    assert.equal(f.calls(), 1);
  });
}

for (const [name, mutate] of [
  ['body actor spoof', (n) => ({ ...n, author: { __typename: 'User', databaseId: 8 } })],
  ['edited', (n) => ({ ...n, lastEditedAt: '2026-01-01' })],
  ['bot', (n) => ({ ...n, author: { __typename: 'Bot', databaseId: 7 } })],
  ['checkpoint mismatch', (n) => ({ ...n, body: JSON.stringify({ ...JSON.parse(n.body), checkpoint: 'wrong' }) })],
  ['head mismatch', (n) => ({ ...n, body: JSON.stringify({ ...JSON.parse(n.body), head: base }) })],
]) test(`rejects ${name}`, async () => {
  const f = fixture(); f.args.record = mutate;
  await assert.rejects(holdCI(f.args), /proof-control: exhausted/);
  assert.equal(f.calls(), 1);
});

test('selected malformed, expired and unreadable controls fail closed', async () => {
  for (const kind of ['json', 'expiry', 'actors', 'api', 'records', 'source']) {
    const f = fixture();
    if (kind === 'expiry') f.cfg.expiresAt = 1000;
    if (kind === 'actors') f.cfg.actorIds = [];
    if (kind === 'source') f.args.identity.workflow = 'bad';
    f.update();
    if (kind === 'json') f.args.raw = '{';
    if (kind === 'api') f.args.github.graphql = async () => { throw new Error('private'); };
    if (kind === 'records') f.args.github.graphql = async () => ({});
    await assert.rejects(holdCI(f.args), /proof-(ci|control): (configuration|transport)/);
  }
});

for (const event of ['pull_request', 'merge_group']) {
  for (const omitted of [['runId'], ['attempt'], ['workflow'], ['head'], ['runId', 'attempt', 'workflow', 'head']]) {
    test(`${event} prospective bridge selector omits ${omitted}`, async () => {
      const f = fixture(event); f.cfg.target = { ...f.cfg.target };
      for (const key of omitted) delete f.cfg.target[key];
      f.update();
      assert.equal(await holdCI(f.args), f.args.result);
      assert.equal(f.calls(), 1, 'prospective selection must reach a fresh authenticated checkpoint');
    });
  }
}

test('optional preselection never makes runtime witness fields optional', async () => {
  for (const key of ['runId', 'attempt', 'workflow', 'head']) {
    const f = fixture(); f.cfg.target = { ...f.cfg.target };
    delete f.cfg.target[key]; delete f.args.identity[key]; f.update();
    await assert.rejects(holdCI(f.args), /proof-ci: configuration/);
    assert.equal(f.calls(), 0);
  }
});

test('hung authenticated read expires within a bounded deadline', { timeout: 2000 }, async () => {
  const f = fixture(); f.cfg.expiresAt = Date.now() + 40; f.update();
  f.args.now = Date.now;
  f.args.github.graphql = () => new Promise(() => {});
  await assert.rejects(holdCI(f.args), /proof-control: expired/);
});

test('unselected tuple and unsupported event are zero-read no-ops', async () => {
  for (const key of ['repo', 'event', 'head', 'checkout', 'workflow', 'job', 'runId', 'attempt', 'prHead']) {
    const f = fixture(); f.cfg.target = { ...f.cfg.target, [key]: 'unrelated' }; f.update();
    assert.equal(await holdCI(f.args), f.args.result); assert.equal(f.calls(), 0);
  }
  const f = fixture('push');
  assert.equal(await holdCI(f.args), f.args.result); assert.equal(f.calls(), 0);
});
