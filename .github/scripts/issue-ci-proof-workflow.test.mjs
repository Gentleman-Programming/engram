import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { createHash } from 'node:crypto';
const dependencies = ['issue-ci-proof-adapter.mjs', 'issue-ci-proof-controls.mjs'];
const digests = await Promise.all(dependencies.map(async (file) => createHash('sha256')
  .update((await readFile(new URL(file, import.meta.url), 'utf8')).replaceAll('\r\n', '\n')).digest('hex')));
const native = await readFile(new URL('../workflows/issue-policy.yml', import.meta.url), 'utf8');
const legacy = await readFile(new URL('../workflows/pr-check.yml', import.meta.url), 'utf8');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
function script(source, label) {
  const step = source.slice(source.indexOf(`      - name: ${label}`)).split(/\r?\n(?:  [\w-]+:|      - name:)/)[0];
  return step.slice(step.indexOf('          script: |') + 19).split(/\r?\n/)
    .filter((line) => line.startsWith('            ')).map((line) => line.slice(12)).join('\n');
}
const sha = 'a'.repeat(40), revision = 'b'.repeat(40);
test('S7 protected workflow bytes pin both newly executed dependencies', () => {
  for (const source of [native]) for (const [index, key] of ['CI_PROOF_ADAPTER_SHA', 'CI_PROOF_CORE_SHA'].entries()) {
    assert.equal(source.split(`${key}: ${digests[index]}`).length - 1, 2, 'both native jobs pin the dependency');
  }
});
for (const [kind, source, names, eventName] of [
  ['native-read', native, ['Evaluate issue reference', 'Evaluate issue approval'], 'pull_request_target'],
  ['legacy-completion', legacy, ['Verify PR links an issue', 'Verify linked issue is approved'], 'pull_request'],
]) for (const [index, policy] of ['reference', 'approved'].entries()) {
  test(`S8 actual ${kind}:${policy} workflow reaches authenticated hold after original read/result`, { timeout: 2000 }, async () => {
    const code = script(source, names[index]);
    assert.match(code, /createGitHubProof/);
    const run = new AsyncFunction('github', 'context', 'core', 'process', code);
    const context = { eventName, repo: { owner: 'owner', repo: 'repo' },
      payload: { action: 'opened', pull_request: { number: 7, state: 'open', body: 'Closes #9', head: { sha } } } };
    const logs = [], failures = [], reads = [];
    let released, checkpoint, checkpointReady;
    const ready = new Promise((resolve) => { checkpointReady = resolve; });
    const github = { rest: { pulls: { get: async () => { reads.push('pull'); return { data: context.payload.pull_request }; } },
      issues: { get: async () => { reads.push('issue'); return { data: { title: 'original', labels: [], assignees: [] } }; } } },
      graphql: async () => new Promise((resolve) => { released = () => resolve({ repository: { issue: { comments: { nodes: [
        { body: JSON.stringify(checkpoint), lastEditedAt: null, author: { __typename: 'User', databaseId: 42 } },
      ] } } } }); }) };
    const raw = { enabled: true, case: 'workflow-test', issueNumber: 9, actorIds: [42],
      expiresAt: Date.now() + 1000, maxReads: 1, intervalMs: 1,
      target: { repo: 'owner/repo', runId: 71, attempt: 2, phase: `${kind}:${policy}`,
        head: sha, source: `${revision}/${revision}`, prNumber: 7, action: 'opened', checkout: revision } };
    const env = { ENGRAM_CI_PROOF_MANIFEST: JSON.stringify(raw), GITHUB_RUN_ID: '71', GITHUB_RUN_ATTEMPT: '2',
      GITHUB_WORKFLOW_SHA: revision, CI_PROOF_CHECKOUT: revision,
      CI_PROOF_ADAPTER_SHA: digests[0], CI_PROOF_CORE_SHA: digests[1],
      GITHUB_WORKSPACE: new URL('../..', import.meta.url).href.replace(/\/$/, '') };
    const pending = run(github, context, { info: (line) => {
      logs.push(line); if (line.startsWith('{')) { const value = JSON.parse(line); if (value.event === 'checkpoint') { checkpoint = value; checkpointReady(); } }
    }, setFailed: (line) => failures.push(line) }, { env });
    // An absent hook must fail, not strand the test waiting for an unimplemented event.
    await Promise.race([ready, pending.then(() => { throw new Error('workflow finished without checkpoint'); })]);
    assert.equal(checkpoint?.event, 'checkpoint');
    assert.equal(typeof released, 'function');
    if (kind === 'native-read') assert.deepEqual(reads, ['pull', 'issue']);
    else assert.equal(failures.length, policy === 'approved' ? 1 : 0, 'original verdict was handled before hold');
    released(); await pending;
    assert.equal(failures.length, policy === 'approved' ? 1 : 0);
    assert.ok(logs.some((line) => line.includes('released')));
  });
  test(`S4 explicit false needs no new adapter on an old trusted base: ${kind}:${policy}`, async () => {
    const code = script(source, names[index]).replace(/import\(/g, 'loadModule(');
    const run = new AsyncFunction('github', 'context', 'core', 'process', 'loadModule', code);
    const pull = { number: 7, state: 'open', body: 'Closes #9', head: { sha } };
    const context = { eventName, repo: { owner: 'owner', repo: 'repo' },
      payload: { action: 'opened', pull_request: pull } };
    const calls = [], failures = [];
    const github = { rest: { pulls: { get: async () => { calls.push('pull'); return { data: pull }; } },
      issues: { get: async () => { calls.push('issue'); return { data: {
        title: 'ready', labels: [{ name: 'status:approved' }], assignees: [{ login: 'owner' }] } }; } } },
      graphql: () => assert.fail('disabled transport must not run') };
    await run(github, context, { info() {}, setFailed: (message) => failures.push(message) },
      { env: { ENGRAM_CI_PROOF_MANIFEST: '{"enabled":false}',
        GITHUB_WORKSPACE: new URL('../..', import.meta.url).href.replace(/\/$/, '') } },
      async (url) => { assert.ok(!url.endsWith('issue-ci-proof-adapter.mjs'), 'old base has no new adapter'); return import(url); });
    assert.deepEqual(failures, []);
    assert.deepEqual(calls, kind === 'native-read' ? ['pull', 'issue', 'pull'] : policy === 'approved' ? ['issue'] : []);
  });
}
