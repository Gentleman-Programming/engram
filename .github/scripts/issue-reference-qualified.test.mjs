import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { evaluateIssueValidation, closingIssueNumbers } from './issue-validation.mjs';
import { evaluateNativeIssuePolicy } from './issue-native-policy.mjs';

const repo = { owner: 'owner', repo: 'repo' };
const sha = 'a'.repeat(40);
const workflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
const scripts = ['Verify PR links an issue', 'Verify linked issue is approved'].map((name) => {
  const step = workflow.slice(workflow.indexOf(`      - name: ${name}`)).split(/\r?\n\r?\n  check-/)[0];
  const body = step.slice(step.indexOf('          script: |') + 19).split(/\r?\n/)
    .filter((line) => line.startsWith('            ')).map((line) => line.slice(12)).join('\n');
  const addressed = body.replace('`${process.env.GITHUB_WORKSPACE}/.github/scripts/merge-queue.mjs`',
    JSON.stringify(new URL('./merge-queue.mjs', import.meta.url).href));
  return new Function('github', 'context', 'core', `return (async () => {${addressed}\n})();`);
});
const ready = () => ({ title: 'Ready', labels: [{ name: 'status:approved' }], assignees: [{ login: 'owner' }] });
async function run(entry, body, { bad, queue = false } = {}) {
  const calls = [], failures = [];
  const pulls = [1, 2].map((number) => ({ number, body, state: 'open', head: { sha } }));
  const deny = new Proxy({}, { get: () => () => assert.fail('SDK write prohibited') });
  const github = { rest: new Proxy({
    pulls: new Proxy({ get: async (params) => ({ data: pulls.find((p) => p.number === params.pull_number) }) }, { get: (obj, key) => obj[key] ?? deny[key] }),
    issues: new Proxy({ get: async (params) => {
      calls.push(params);
      if (params.issue_number === 10 && bad === 'fetch') throw new Error('offline');
      const value = ready();
      if (params.issue_number === 10) {
        if (bad === 'approval') value.labels = [];
        if (bad === 'owner') value.assignees = [];
        if (bad === 'labels') value.labels.push({});
        if (bad === 'assignees') value.assignees = [{}];
      }
      return { data: value };
    } }, { get: (obj, key) => obj[key] ?? deny[key] }),
  }, { get: (obj, key) => obj[key] ?? deny }), graphql: async () => ({ repository: { mergeQueue: { entries: {
    nodes: pulls.map((p) => ({ position: p.number, pullRequest: { number: p.number } })), pageInfo: { hasNextPage: false },
  } } } }) };
  const context = { repo, eventName: queue ? 'merge_group' : 'pull_request', payload: { action: 'opened', pull_request: pulls[0], merge_group: {
    base_ref: 'refs/heads/main', head_ref: `refs/heads/gh-readonly-queue/main/pr-2-${sha}`,
  } } };
  let verdicts;
  if (entry === 'shared') {
    const [result] = await evaluateIssueValidation(github, context);
    verdicts = [result.reference.conclusion, result.approved.conclusion];
  } else if (entry.startsWith('native')) {
    const result = await evaluateNativeIssuePolicy(github, { ...context, eventName: 'pull_request_target' }, entry.split('-')[1]);
    verdicts = [result.conclusion];
  } else {
    await scripts[entry === 'legacy-reference' ? 0 : 1](github, context, { info() {}, setFailed: (message) => failures.push(message) });
    verdicts = [failures.length ? 'failure' : 'success'];
  }
  return { calls, verdicts, failures };
}
const entries = ['shared', 'native-reference', 'native-approved', 'legacy-reference', 'legacy-approved'];
for (const entry of entries) {
  for (const identity of ['owner/repo', 'OWNER/REPO', 'Gentleman-Programming/engram', 'gentleman-programming/ENGRAM']) {
    test(`${entry} resolves trusted ${identity} and duplicates`, async () => {
      const body = `preCLOSES ${identity}#9; fixes\n${identity}#10; resolves ${identity}#9; closes#11`;
      const actual = await run(entry, body);
      assert.ok(actual.verdicts.every((v) => v === 'success'));
      assert.deepEqual(actual.calls, entry === 'legacy-reference' ? [] : [
        { owner: identity.split('/')[0], repo: identity.split('/')[1], issue_number: 9 },
        { owner: identity.split('/')[0], repo: identity.split('/')[1], issue_number: 10 },
        { owner: identity.split('/')[0], repo: identity.split('/')[1], issue_number: 9 },
      ]);
    });
  }
  for (const prefix of ['', 'Closes #9; ', 'Fixes owner/repo#9; ']) test(`${entry} preflights untrusted after ${prefix}`, async () => {
    const actual = await run(entry, `${prefix}resolves attacker/other#10`);
    assert.ok(actual.verdicts.every((v) => v === 'failure'));
    assert.deepEqual(actual.calls, []);
  });
  for (const bad of ['approval', 'owner', 'fetch', 'labels', 'assignees']) test(`${entry} checks every qualified ref: ${bad}`, async () => {
    const actual = await run(entry, 'Closes #9; fixes Gentleman-Programming/engram#10', { bad });
    const approvalEntry = entry === 'shared' || entry.endsWith('approved');
    assert.equal(actual.verdicts.at(-1), approvalEntry ? 'failure' : 'success');
    assert.equal(actual.calls.length, entry === 'legacy-reference' ? 0 : 2);
  });
}
for (const entry of ['legacy-reference', 'legacy-approved']) test(`${entry} validates every queued PR`, async () => {
  const actual = await run(entry, 'Closes #9; fixes attacker/other#10', { queue: true });
  assert.deepEqual(actual.calls, []);
  assert.match(actual.failures.join('\n'), /PR #1:/);
  assert.match(actual.failures.join('\n'), /PR #2:/);
});
for (const entry of ['legacy-reference', 'legacy-approved']) test(`${entry} accepts qualified refs in every queued PR`, async () => {
  const actual = await run(entry, 'Closes Gentleman-Programming/engram#9', { queue: true });
  assert.deepEqual(actual.verdicts, ['success']);
  assert.deepEqual(actual.calls, entry === 'legacy-reference' ? [] : [1, 2].map(() => ({
    owner: 'Gentleman-Programming', repo: 'engram', issue_number: 9,
  })));
});
for (const entry of ['legacy-reference', 'legacy-approved']) {
  test(`${entry} accepts bare and qualified refs keyword`, async () => {
    const actual = await run(entry, 'Refs #9; refs owner/repo#10; REFS Gentleman-Programming/engram#11');
    assert.deepEqual(actual.verdicts, ['success']);
    assert.deepEqual(actual.calls, entry === 'legacy-reference' ? [] : [
      { owner: 'owner', repo: 'repo', issue_number: 9 },
      { owner: 'owner', repo: 'repo', issue_number: 10 },
      { owner: 'Gentleman-Programming', repo: 'engram', issue_number: 11 },
    ]);
  });
  test(`${entry} rejects untrusted qualified refs keyword`, async () => {
    const actual = await run(entry, 'Refs #9; refs attacker/other#10');
    assert.deepEqual(actual.verdicts, ['failure']);
    assert.deepEqual(actual.calls, []);
  });
  test(`${entry} accepts refs keyword in every queued PR`, async () => {
    const actual = await run(entry, 'Refs #9; refs owner/repo#10', { queue: true });
    assert.deepEqual(actual.verdicts, ['success']);
    assert.deepEqual(actual.calls, entry === 'legacy-reference' ? [] : [
      { owner: 'owner', repo: 'repo', issue_number: 9 },
      { owner: 'owner', repo: 'repo', issue_number: 9 },
      { owner: 'owner', repo: 'repo', issue_number: 10 },
      { owner: 'owner', repo: 'repo', issue_number: 10 },
    ]);
  });
}
test('discovery alias remains bare-only with unchanged permissive grammar', () => {
  assert.deepEqual(closingIssueNumbers('preCLOSES #9; fixes\n#10; resolves #9; closes#11; closes owner/repo#12'), [9, 10, 9]);
});
