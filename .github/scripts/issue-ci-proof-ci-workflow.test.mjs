import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { holdCI } from './issue-ci-proof-ci.mjs';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const block = source.split('      - name: Await selected CI proof\n')[1].split('      - name: Checkout\n')[0];
const script = block.split('          script: |\n')[1].replace(/^            /gm, '');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const run = new AsyncFunction('context', 'github', 'core', 'process', 'require', script);
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
    if (h.missing) throw new Error('missing');
    // GitHub returns Git blob bytes, not Windows checkout CRLF conversions.
    const content = Buffer.from(readFileSync(new URL(`./${path.basename(request.path)}`, import.meta.url), 'utf8').replaceAll('\r\n', '\n'));
    return { data: { encoding: 'base64', content: (h.corrupt ? Buffer.from('bad') : content).toString('base64') } };
  } } }, graphql: async () => {
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
      assert.equal(files.size, 2);
      return holdCI(args);
    } };
    return { 'node:path': path.posix, 'node:crypto': { createHash } }[name];
  };
  const h = { cfg, env, context, trace, files, run: () => run(context, github,
    { info: (line) => { checkpoint = JSON.parse(line); } }, { env }, fakeRequire) };
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
test('hold precedes original commands; only Unit Tests receives read permission', () => {
  const unit = source.split('  unit-tests:\n')[1].split('  e2e-tests:\n')[0];
  assert.match(unit, /name: Unit Tests\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      issues: read/);
  assert.ok(unit.indexOf('Await selected CI proof') < unit.indexOf('name: Checkout'));
  for (const command of ['make templ', 'make deadcode-check', 'go test ./...', './engram-selftest test --quick']) assert.ok(unit.includes(command));
  assert.doesNotMatch(unit, /continue-on-error|environment:|\n    if:|\n        if:/);
  assert.equal((source.match(/issues: read/g) ?? []).length, 1);
  assert.match(source, /permissions:\n  contents: read\n/);
  assert.match(source, /pull_request:\n  merge_group:\n    types: \[checks_requested\]/);
});
