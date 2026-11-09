import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { readCIConfiguration } from './issue-ci-proof-config.mjs';

const digest = (s) => createHash('sha256').update(s).digest('hex');
function harness() {
  const b = { repo: 'Gentleman-Programming/engram', event: 'pull_request', action: 'synchronize',
    job: 'unit-tests', head: 'a'.repeat(40), checkout: 'b'.repeat(40), workflow: 'c'.repeat(40),
    runId: 12, attempt: 1, prHead: 'd'.repeat(40) };
  const manifest = { enabled: true, case: 'c03', issueNumber: 2049, target: { ...b },
    actorIds: [7], expiresAt: 1200, maxReads: 1, intervalMs: 1 };
  const envelope = { schema: 'ci-proof-config/v1', prNumber: 2052, manifest };
  const node = { body: JSON.stringify(envelope), lastEditedAt: null,
    author: { __typename: 'User', databaseId: 7 } };
  const h = { b, manifest, envelope, node, nodes: [node], policy: { version: 1, controllerIds: [7] },
    calls: [], clock: 1000, deadline: 1100 };
  const github = { rest: { repos: { getContent: async (r) => {
    h.calls.push(r); if (h.error) throw new Error('PRIVATE ERROR BODY');
    h.clock = h.afterPolicy ?? h.clock;
    return { data: { type: 'file', sha: 'e'.repeat(40), encoding: 'base64',
      content: Buffer.from(JSON.stringify(h.policy)).toString('base64') } };
  } } }, graphql: async (q, r) => {
    h.calls.push({ query: q, ...r }); h.clock = h.afterComments ?? h.clock;
    return { repository: { issue: { comments: { nodes: h.nodes } } } };
  } };
  h.run = (extra = {}) => readCIConfiguration({ github, identity: b, issueNumber: 2049,
    caseName: 'c03', prNumber: 2052, deadline: h.deadline, now: () => h.clock, ...extra });
  h.update = () => { node.body = JSON.stringify(envelope); };
  return h;
}

test('reads current canonical policy, authenticates a snapshot and returns only safe provenance', async () => {
  const h = harness(); const got = await h.run();
  assert.deepEqual(JSON.parse(got.raw), h.manifest);
  assert.deepEqual(got.receipt, { event: 'configuration-resolved', controllerId: 7,
    policyBlob: 'e'.repeat(40), policySha256: digest(JSON.stringify(h.policy)), configSha256: digest(h.node.body) });
  assert.equal(h.calls.length, 2);
  assert.deepEqual(Object.fromEntries(Object.entries(h.calls[0]).filter(([k]) => k !== 'request')),
    { owner: 'Gentleman-Programming', repo: 'engram', path: '.github/ci-proof-controllers.json', ref: 'main' });
  assert.match(h.calls[1].query, /comments\(last: 20\)/);
  assert.equal(h.calls[1].owner, 'Gentleman-Programming'); assert.equal(h.calls[1].issue, 2049);
  for (const r of h.calls) assert.ok(r.request.timeout > 0 && r.request.timeout <= 100);
});
for (const [name, mutate] of [
  ['empty roster', h => { h.policy.controllerIds = []; }],
  ['historical controller removed on main', h => { h.policy.controllerIds = [8]; }],
  ['bad policy version', h => { h.policy.version = 2; }],
  ['bad policy identity', h => { h.policy.controllerIds = ['7']; }],
  ['forged JSON identity', h => { h.node.author.databaseId = 8; h.envelope.actorId = 7; h.update(); }],
  ['bot', h => { h.node.author.__typename = 'Bot'; }],
  ['deleted author', h => { h.node.author = null; }],
  ['edited', h => { h.node.lastEditedAt = '2026-10-23T00:00:00Z'; }],
  ['missing snapshot', h => { h.nodes = []; }],
  ['ambiguous snapshot', h => { h.nodes.push(structuredClone(h.node)); }],
  ['oversized snapshot', h => { h.nodes = Array(21).fill(h.node); }],
  ['oversized body', h => { h.node.body = ' '.repeat(8193); }],
  ['wrong PR', h => { h.envelope.prNumber++; h.update(); }],
  ['wrong original head', h => { h.manifest.target.prHead = 'f'.repeat(40); h.update(); }],
  ['wrong case', h => { h.manifest.case = 'other'; h.update(); }],
  ['disabled selected manifest', h => { h.manifest.enabled = false; h.update(); }],
  ['wrong release issue', h => { h.manifest.issueNumber++; h.update(); }],
  ['expired', h => { h.manifest.expiresAt = 1000; h.update(); }],
  ['overlong lifetime', h => { h.manifest.expiresAt = 301001; h.update(); }],
  ['empty release actors', h => { h.manifest.actorIds = []; h.update(); }],
  ['invalid read limit', h => { h.manifest.maxReads = 11; h.update(); }],
  ['invalid interval', h => { h.manifest.intervalMs = 1001; h.update(); }],
]) test(`rejects ${name} without logging or writes`, async () => {
  const h = harness(); mutate(h); await assert.rejects(h.run(), /^Error: proof-config: (configuration|source|expired)$/);
  assert.ok(h.calls.length <= 2);
});
for (const key of ['repo', 'event', 'action', 'job', 'checkout', 'runId', 'attempt', 'workflow', 'head']) {
  test(`rejects supplied mismatching selector ${key}`, async () => {
    const h = harness(); h.manifest.target[key] = typeof h.b[key] === 'number' ? 99 : 'wrong'; h.update();
    await assert.rejects(h.run(), /proof-config: configuration/);
  });
}
test('optional future selectors may be omitted; unrelated and unauthorized comments do not poison selection', async () => {
  const h = harness(); for (const k of ['runId', 'attempt', 'workflow', 'head']) delete h.manifest.target[k]; h.update();
  h.nodes.unshift({ ...h.node, author: { __typename: 'User', databaseId: 99 } },
    { ...h.node, body: '{bad json' }, { ...h.node, body: '{"event":"released"}' },
    { ...h.node, body: JSON.stringify({ ...h.envelope, prNumber: 1 }) });
  assert.deepEqual(JSON.parse((await h.run()).raw), h.manifest);
});
for (const at of ['afterPolicy', 'afterComments']) test(`rejects late acceptance ${at}`, async () => {
  const h = harness(); h[at] = 1100; await assert.rejects(h.run(), /proof-config: expired/);
  assert.equal(h.calls.length, at === 'afterPolicy' ? 1 : 2);
});
test('expiry during validation cannot return a late receipt', async () => {
  const h = harness(); let reads = 0;
  await assert.rejects(h.run({ now: () => ++reads >= 7 ? 1200 : 1000 }), /proof-config: expired/);
});
test('transport errors are redacted and never retried', async () => {
  const h = harness(); h.error = true; await assert.rejects(h.run(), /^Error: proof-config: transport$/);
  assert.equal(h.calls.length, 1);
});
test('hung read terminates with injected timer; late results cannot return a configuration', async () => {
  const h = harness(); let finish; let timer;
  const github = { rest: { repos: { getContent: () => new Promise(r => { finish = r; }) } } };
  const pending = h.run({ github, timers: { setTimeout: fn => { timer = fn; return 1; }, clearTimeout() {} } });
  const rejected = assert.rejects(pending, /proof-config: transport/);
  if (timer) timer();
  await rejected; finish?.({ data: {} });
});
test('repository policy enrolls nobody', () => {
  assert.deepEqual(JSON.parse(readFileSync(new URL('../ci-proof-controllers.json', import.meta.url))),
    { version: 1, controllerIds: [] });
});

for (const action of [undefined, null, 1]) test(`untyped action ${action} is rejected before policy API`, async () => {
  const h = harness(); h.b.action = action;
  await assert.rejects(h.run(), /^Error: proof-config: configuration$/); assert.deepEqual(h.calls, []);
});
