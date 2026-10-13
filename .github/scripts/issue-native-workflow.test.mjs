import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const source = await readFile(new URL('../workflows/issue-policy.yml', import.meta.url), 'utf8');
for (const newline of ['LF', 'CRLF']) test(`${newline} parser and wrappers`, async (t) => {
const input = source.replace(/\r\n/g, '\n').replace(/\n/g, newline === 'CRLF' ? '\r\n' : '\n');
// Normalize only the test parser input, never the workflow on disk.
const workflow = input.replace(/\r\n/g, '\n');
const jobs = workflow.split('jobs:\n')[1].split(/^  [\w-]+:\n/gm).slice(1);
await t.test(`${newline}: native workflow has exactly two independent read-only trusted jobs`, () => {
  assert.equal(jobs.length, 2);
  assert.match(workflow, /types: \[opened, edited, labeled, unlabeled, synchronize, reopened\]/);
  assert.match(workflow, /^permissions: \{\}$/m);
  assert.doesNotMatch(workflow, /needs:|merge_group|write|checks:|if:|continue-on-error|github\.event|github\.sha/);
  assert.deepEqual(jobs.map((job) => job.match(/name: (Check[^\n]+)/)[1]), ['Check Issue Reference', 'Check Issue Has status:approved']);
  for (const job of jobs) {
    assert.match(job, /permissions:\n      contents: read\n      issues: read\n      pull-requests: read/);
    assert.match(job, /name: Load trusted issue policy \(\$\{\{ github.workflow_sha \}\}\)/);
    assert.match(job, /uses: actions\/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6\n/);
    assert.match(job, /ref: \$\{\{ github.workflow_sha \}\}\n          persist-credentials: false/);
    assert.match(job, /uses: actions\/github-script@ed597411d8f924073f98dfc5c65a23a2325f34cd # v8\n/);
    assert.match(job, /retries: 0/);
  }
});
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
for (const [index, policy] of ['reference', 'approved'].entries()) {
  await t.test(`${newline}: actual ${policy} job wrapper reports verdicts and fail-closed errors`, async () => {
    const script = jobs[index].split('          script: |\n')[1].split('\n').map((line) => line.startsWith('            ') ? line.slice(12) : line).join('\n');
    assert.doesNotMatch(script, /\$\{\{/);
    const run = new AsyncFunction('github', 'context', 'core', 'process', script);
    const sha = 'a'.repeat(40);
    const context = { repo: { owner: 'owner', repo: 'repo' }, eventName: 'pull_request_target', payload: { action: 'opened', pull_request: { number: 1, head: { sha } } } };
    for (const scenario of ['approved', 'revoked', 'missing', 'drift', 'error', 'null-error']) {
      const log = [], failures = [], requests = [];
      const github = { rest: {
        pulls: { get: async (params) => {
          requests.push(['pulls.get', params]);
          if (scenario === 'error') throw new Error('offline');
          if (scenario === 'null-error') throw null;
          return { data: { number: 1, state: 'open', body: scenario === 'missing' ? '' : 'Closes #9', head: { sha: scenario === 'drift' ? 'b'.repeat(40) : sha } } };
        } },
        issues: { get: async (params) => { requests.push(['issues.get', params]); return { data: { labels: scenario === 'revoked' ? [] : [{ name: 'status:approved' }] } }; } },
        checks: { create: () => assert.fail('prohibited publication') },
      } };
      let exitCode = 0;
      await run(github, context, { info: (message) => log.push(message), setFailed: (message) => { failures.push(message); exitCode = 1; } }, { env: { GITHUB_WORKSPACE: new URL('../..', import.meta.url).href.replace(/\/$/, '') } });
      const fails = ['missing', 'drift', 'error', 'null-error'].includes(scenario) || (policy === 'approved' && scenario === 'revoked');
      assert.equal(failures.length, fails ? 1 : 0, scenario);
      assert.equal(exitCode, fails ? 1 : 0);
      if (fails) assert.equal(typeof failures[0], 'string');
      if (scenario === 'error') assert.deepEqual(failures, ['offline']);
      if (scenario === 'null-error') assert.deepEqual(failures, ['null']);
      if (scenario === 'drift') assert.deepEqual(failures, ['Native PR head drift; fresh run required']);
      if (scenario === 'missing') assert.deepEqual(failures, ['PR #1: no closing issue reference found']);
      if (scenario === 'revoked' && policy === 'approved') assert.deepEqual(failures, ['PR #1: issue #9 lacks status:approved']);
      if (['error', 'null-error', 'drift'].includes(scenario)) assert.equal(log.length, 0);
      else assert.deepEqual(log, [`${policy}: ${fails ? 'failure' : 'success'} (${sha})`]);
      assert.deepEqual(requests[0], ['pulls.get', { ...context.repo, pull_number: 1 }]);
      assert.equal(requests.length, ['error', 'null-error'].includes(scenario) ? 1 : scenario === 'missing' ? 2 : 3);
    }
  });
}
});
