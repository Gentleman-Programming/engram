import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { holdCI } from './issue-ci-proof-ci.mjs';
import { readCIConfiguration } from './issue-ci-proof-config.mjs';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const block = source.split('      - name: Await selected CI proof\n')[1].split('      - name: Checkout\n')[0];
const script = block.split('          script: |\n')[1].replace(/^            /gm, '');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const run = new AsyncFunction('context', 'github', 'core', 'process', 'require', 'Date', 'setTimeout', 'clearTimeout', script);
const sha = 'a'.repeat(40), base = 'b'.repeat(40), workflow = 'c'.repeat(40);

export function workflowHarness(event = 'pull_request', raw) {
  const identity = { repo: 'owner/repo', event, action: event === 'merge_group' ? 'checks_requested' : 'synchronize',
    job: 'unit-tests', head: sha, checkout: base, workflow, runId: 12, attempt: 2,
    prHead: event === 'pull_request' ? 'd'.repeat(40) : null };
  const cfg = { enabled: true, target: identity, case: 'independent', issueNumber: 42,
    actorIds: [7], expiresAt: Date.now() + 1500, maxReads: 1, intervalMs: 1 };
  const context = { repo: { owner: 'owner', repo: 'repo' }, eventName: event, sha,
    payload: { action: identity.action, ...(event === 'merge_group'
      ? { merge_group: { base_sha: base, head_sha: sha } }
      : { pull_request: { base: { sha: base }, head: { sha: identity.prHead } } }) } };
  const trace = [], files = new Map();
  let checkpoint;
  const github = { rest: { repos: { getContent: async (request) => {
    trace.push(request);
    if (h.getContent) return h.getContent(request);
    if (h.missing) throw new Error('missing');
    if (request.path === '.github/ci-proof-controllers.json') return { data: { type: 'file',
      sha: 'e'.repeat(40), encoding: 'base64', content: Buffer.from(JSON.stringify({ version: 1,
        controllerIds: h.controllers ?? [] })).toString('base64') } };
    // GitHub returns Git blob bytes, not Windows checkout CRLF conversions.
    const content = Buffer.from(readFileSync(new URL(`./${path.basename(request.path)}`, import.meta.url), 'utf8').replaceAll('\r\n', '\n'));
    return { data: { encoding: 'base64', content: (h.corrupt ? Buffer.from('bad') : content).toString('base64') } };
  } } }, graphql: async (query) => {
    if (query.includes('CIConfiguration')) {
      trace.push('configuration');
      return { repository: { issue: { comments: { nodes: [{ lastEditedAt: null,
        author: { __typename: 'User', databaseId: 7 }, body: JSON.stringify({
          schema: 'ci-proof-config/v1', prNumber: 2052, manifest: cfg }) }] } } } };
    }
    trace.push('comments');
    return { repository: { issue: { comments: { nodes: [{ body: JSON.stringify(checkpoint),
      lastEditedAt: null, author: { __typename: 'User', databaseId: 7 } }] } } } };
  } };
  const env = { CI_PROOF_CONTROL: raw, GITHUB_JOB: identity.job, GITHUB_WORKFLOW_SHA: workflow,
    GITHUB_RUN_ID: '12', GITHUB_RUN_ATTEMPT: '2', RUNNER_TEMP: '/isolated-fake' };
  const fakeRequire = (name) => {
    if (name === 'node:fs') return { mkdtempSync: () => '/isolated-fake/source',
      writeFileSync: (file, bytes) => { assert.ok(!files.has(file)); files.set(file, bytes); } };
    if (name.endsWith('/issue-ci-proof-ci.mjs')) return { holdCI: (args) => {
      assert.deepEqual(args.identity, identity);
      assert.equal(files.size, h.controllers ? 3 : 2);
      return holdCI(args);
    } };
    if (name.endsWith('/issue-ci-proof-config.mjs')) return { readCIConfiguration };
    return { 'node:path': path.posix, 'node:crypto': { createHash } }[name];
  };
  const h = { cfg, env, context, trace, files, identity, events: [], run: () => run(context, github,
    { info: (line) => { checkpoint = JSON.parse(line); h.events.push(checkpoint); } }, { env }, fakeRequire,
    h.Date ?? Date, h.setTimeout ?? setTimeout, h.clearTimeout ?? clearTimeout) };
  return h;
}

for (const event of ['pull_request', 'merge_group', 'push']) {
  test(`${event} OFF and unselected do not load old-base helpers or call APIs`, async () => {
    for (const raw of [undefined, '{"enabled":false}', '{"enabled":true,"target":{"repo":"other/repo"}}']) {
      const h = workflowHarness(event, raw); h.missing = true;
      await h.run(); assert.deepEqual(h.trace, []); assert.equal(h.files.size, 0);
    }
  });
}
for (const event of ['pull_request', 'merge_group']) {
  test(`${event} loads only pinned base implementation and binds runtime`, async () => {
    const h = workflowHarness(event); h.env.CI_PROOF_CONTROL = JSON.stringify(h.cfg);
    await h.run();
    assert.equal(h.trace.length, 3);
    for (const request of h.trace.slice(0, 2)) assert.equal(request.ref, base);
    assert.equal(h.trace[2], 'comments');
  });
}
for (const event of ['pull_request', 'merge_group']) {
  for (const omitted of [['runId'], ['attempt'], ['workflow'], ['head'], ['runId', 'attempt', 'workflow', 'head']]) {
    test(`${event} prospective bootstrap selector omits ${omitted}`, async () => {
      const h = workflowHarness(event); h.cfg.target = { ...h.cfg.target };
      for (const key of omitted) delete h.cfg.target[key];
      h.env.CI_PROOF_CONTROL = JSON.stringify(h.cfg);
      await h.run();
      assert.equal(h.trace.length, 3, 'must load trusted blobs and reach authenticated checkpoint');
      assert.equal(h.trace[2], 'comments');
    });
  }
}

test('selected missing, corrupted source and merge-group head confusion fail same step', async () => {
  for (const kind of ['missing', 'corrupt', 'group']) {
    const h = workflowHarness('merge_group'); h.env.CI_PROOF_CONTROL = JSON.stringify(h.cfg);
    if (kind === 'group') h.context.payload.merge_group.head_sha = 'd'.repeat(40);
    else h[kind] = true;
    await assert.rejects(h.run(), /proof-ci: bootstrap/);
    assert.ok(!h.trace.includes('comments'));
  }
});
test('hold precedes checkout; only Unit Tests receives issues read permission', () => {
  const unit = source.split('  unit-tests:\n')[1].split('  e2e-tests:\n')[0];
  assert.match(unit, /name: Unit Tests\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      issues: read/);
  assert.ok(unit.indexOf('Await selected CI proof') < unit.indexOf('name: Checkout'));
  // Keep proof-step escape checks here; technical command checks have no hold dependency.
  assert.doesNotMatch(block, /continue-on-error|environment:|\n    if:|\n        if:/);
  assert.equal((source.match(/issues: read/g) ?? []).length, 1);
});

function proofHarness() {
  const h = workflowHarness(); h.controllers = [7]; h.cfg.case = 'c03'; h.cfg.issueNumber = 2049;
  h.identity.repo = 'Gentleman-Programming/engram';
  h.context.repo = { owner: 'Gentleman-Programming', repo: 'engram' };
  Object.assign(h.context.payload.pull_request, { number: 2052,
    base: { sha: base, ref: 'main', repo: { full_name: h.identity.repo } },
    head: { sha: h.identity.prHead, repo: { full_name: 'fixture/engram' } } });
  h.env.GITHUB_HEAD_REF = 'ci/proof-2049-c03';
  return h;
}
test('fork opt-in delivers authenticated configuration then real checkpoint and release', async () => {
  const h = proofHarness(); await h.run();
  assert.deepEqual(h.events.map(r => r.event), ['configuration-resolved', 'checkpoint', 'released']);
  assert.equal(h.trace.length, 6);
  for (const r of h.trace.slice(0, 3)) assert.equal(r.ref, base);
  assert.equal(h.trace[3].ref, 'main'); assert.equal(h.trace[4], 'configuration');
  assert.equal(h.trace[5], 'comments'); assert.equal(h.events[1].head, sha);
});
test('malformed reserved routing fails before imports or APIs', async () => {
  for (const ref of ['ci/proof-0-c03', 'ci/proof-01-c03', 'ci/proof-2147483648-c03',
    'ci/proof-2049-', 'ci/proof-2049-UPPER', `ci/proof-2049-${'a'.repeat(101)}`]) {
    const h = proofHarness(); h.env.GITHUB_HEAD_REF = ref;
    await assert.rejects(h.run(), /proof-ci: bootstrap/); assert.deepEqual(h.trace, []);
  }
});
test('any raw source including explicit false conflicts with opt-in before APIs', async () => {
  for (const raw of ['false', '{"enabled":false}', '{"enabled":true}']) {
    const h = proofHarness(); h.env.CI_PROOF_CONTROL = raw;
    await assert.rejects(h.run(), /proof-ci: bootstrap/); assert.deepEqual(h.trace, []);
  }
});
test('opt-in rejects missing identity or noncanonical/nonfork sources before APIs', async () => {
  for (const mutate of [h => { h.context.payload.pull_request.base.ref = 'other'; },
    h => { h.context.payload.pull_request.base.repo.full_name = 'fixture/engram'; },
    h => { h.context.payload.pull_request.head.repo.full_name = h.identity.repo; },
    h => { h.context.payload.pull_request.head.repo.full_name = ''; },
    h => { delete h.context.payload.pull_request.number; }, h => { h.env.GITHUB_RUN_ID = 'bad'; },
    h => { h.context.payload.pull_request.head.sha = 'bad'; }]) {
    const h = proofHarness(); mutate(h);
    await assert.rejects(h.run(), /proof-ci: bootstrap/); assert.deepEqual(h.trace, []);
  }
});
test('branch hint never activates merge_group or push', async () => {
  for (const event of ['merge_group', 'push']) {
    const h = workflowHarness(event); h.env.GITHUB_HEAD_REF = 'ci/proof-2049-c03';
    await h.run(); assert.deepEqual(h.trace, []); assert.deepEqual(h.events, []);
  }
});
test('empty canonical roster fails selected job, never becomes an OFF success', async () => {
  const h = proofHarness(); h.controllers = [];
  await assert.rejects(h.run(), /proof-ci: bootstrap/);
  assert.equal(h.trace.length, 4); assert.deepEqual(h.events, []);
});
test('late source response cannot write or emit', async () => {
  const h = proofHarness(); let clock = 1000; h.Date = { now: () => clock };
  h.getContent = async () => { clock += 15000; return { data: {} }; };
  await assert.rejects(h.run(), /proof-ci: bootstrap/);
  assert.equal(h.trace.length, 1); assert.equal(h.files.size, 0); assert.deepEqual(h.events, []);
});

test('hung source import terminates; a late response cannot resume imports or emit', async () => {
  const h = proofHarness(); let finish, expire;
  h.getContent = () => new Promise(r => { finish = r; });
  h.setTimeout = fn => { expire = fn; return 1; }; h.clearTimeout = () => {};
  const pending = h.run(); const rejected = assert.rejects(pending, /^Error: proof-ci: bootstrap$/);
  expire(); await rejected; finish({ data: {} }); await Promise.resolve();
  assert.equal(h.trace.length, 1); assert.equal(h.files.size, 0); assert.deepEqual(h.events, []);
});

test('untyped action is rejected before source imports and APIs', async () => {
  for (const action of [undefined, null, 1]) {
    const h = proofHarness(); h.context.payload.action = action;
    await assert.rejects(h.run(), /^Error: proof-ci: bootstrap$/);
    assert.deepEqual(h.trace, []); assert.equal(h.files.size, 0); assert.deepEqual(h.events, []);
  }
});
