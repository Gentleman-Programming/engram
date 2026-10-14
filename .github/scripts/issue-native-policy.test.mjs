import test from 'node:test';
import assert from 'node:assert/strict';
import { evaluateNativeIssuePolicy } from './issue-native-policy.mjs';
import { closingIssueNumbers, evaluateIssueValidation } from './issue-validation.mjs';

const sha = 'a'.repeat(40);
const repo = { owner: 'owner', repo: 'repo' };
const context = (action = 'opened') => ({ repo, eventName: 'pull_request_target', sha: 'b'.repeat(40), payload: { action, pull_request: { number: 1, head: { sha }, body: 'Closes #999' } } });
function fake({ body = 'Closes #9', labels = ['status:approved'], head = sha, change, fail, state = 'open', assignees = [{ login: 'owner-not-author' }] } = {}) {
  const requests = [];
  const github = { rest: {
    pulls: { get: async (params) => {
      requests.push(['pulls.get', params]);
      if (fail === 'initial' || (fail === 'final' && requests.length > 1)) throw new Error('offline');
      return { data: { number: 1, body, state, head: { sha: change && requests.length > 1 ? change : head } } };
    } },
    issues: { get: async (params) => {
      requests.push(['issues.get', params]);
      if (fail === 'issue') throw new Error('offline');
      return { data: { labels: labels.map((name) => ({ name })), assignees } };
    } },
  } };
  for (const namespace of ['checks', 'repos', 'actions']) github.rest[namespace] = new Proxy({}, { get: () => () => assert.fail('SDK write prohibited') });
  for (const namespace of ['pulls', 'issues']) for (const method of ['create', 'update', 'addLabels', 'removeLabel']) github.rest[namespace][method] = () => assert.fail('SDK write prohibited');
  return { github, requests };
}
const expected = (numbers = [9]) => [
  ['pulls.get', { ...repo, pull_number: 1 }],
  ...numbers.map((issue_number) => ['issues.get', { ...repo, issue_number }]),
  ['pulls.get', { ...repo, pull_number: 1 }],
];
for (const action of ['opened', 'edited', 'labeled', 'unlabeled', 'synchronize', 'reopened']) {
  test(`native ${action} binds original head, reads current body and labels`, async () => {
    const f = fake();
    assert.deepEqual(await evaluateNativeIssuePolicy(f.github, context(action), 'approved'), { headSha: sha, conclusion: 'success', summary: 'Validation passed.' });
    assert.deepEqual(f.requests, expected());
  });
}
for (const policy of ['reference', 'approved']) for (const labels of [[], ['status:approved']]) {
  test(`${policy} selects independent verdict with current approval ${labels.length}`, async () => {
    const f = fake({ labels, body: 'FIXES\n#9; resolves #10' });
    const result = await evaluateNativeIssuePolicy(f.github, context(), policy);
    assert.equal(result.conclusion, policy === 'reference' || labels.length ? 'success' : 'failure');
    if (result.conclusion === 'failure') assert.match(result.summary, /issue #10 lacks status:approved/);
    assert.deepEqual(f.requests, expected([9, 10]));
  });
}
for (const policy of ['reference', 'approved']) test(`missing reference fails ${policy}`, async () => {
  const f = fake({ body: 'mentions #9' });
  assert.match((await evaluateNativeIssuePolicy(f.github, context(), policy)).summary, /no closing issue reference/);
  assert.deepEqual(f.requests, expected([]));
});
test('native approval rejects an unassigned issue while reference remains valid', async () => {
  for (const policy of ['reference', 'approved']) {
    const f = fake({ assignees: [] });
    assert.deepEqual(await evaluateNativeIssuePolicy(f.github, context(), policy), {
      headSha: sha, conclusion: policy === 'approved' ? 'failure' : 'success',
      summary: policy === 'approved' ? 'PR #1: issue #9 has no assignee. Assign an owner before merge.' : 'Validation passed.',
    });
    assert.deepEqual(f.requests, expected());
  }
});
test('invalid native admission rejects without SDK requests', async () => {
  for (const ctx of [{ ...context(), eventName: 'pull_request' }, context('closed'), { ...context(), payload: { action: 'opened', pull_request: { number: 1, head: { sha: 'invalid' } } } }]) {
    const f = fake();
    await assert.rejects(evaluateNativeIssuePolicy(f.github, ctx, 'approved'), /native|original/i);
    assert.deepEqual(f.requests, []);
  }
  const f = fake();
  await assert.rejects(evaluateNativeIssuePolicy(f.github, context(), 'other'), /policy/i);
  assert.deepEqual(f.requests, []);
});
test('missing target and original-versus-fetched head drift reject explicitly', async () => {
  for (const options of [{ state: 'closed' }, { head: 'c'.repeat(40) }]) {
    const f = fake(options);
    await assert.rejects(evaluateNativeIssuePolicy(f.github, context(), 'approved'), /target|head/i);
    assert.equal(f.requests[0][1].pull_number, 1);
  }
});
for (const fail of ['initial', 'final', 'issue']) test(`metadata error ${fail} cannot approve`, async () => {
  const f = fake({ fail });
  if (fail === 'initial') await assert.rejects(evaluateNativeIssuePolicy(f.github, context(), 'approved'), /offline/);
  else assert.equal((await evaluateNativeIssuePolicy(f.github, context(), 'approved')).conclusion, 'failure');
  assert.deepEqual(f.requests, fail === 'initial' ? expected().slice(0, 1) : expected());
});
test('final head change fails both independent policies', async () => {
  for (const policy of ['reference', 'approved']) {
    const f = fake({ change: 'c'.repeat(40) });
    const result = await evaluateNativeIssuePolicy(f.github, context(), policy);
    assert.equal(result.headSha, sha);
    assert.equal(result.conclusion, 'failure');
    assert.match(result.summary, /metadata changed/);
    assert.deepEqual(f.requests, expected());
  }
});
test('additive parser alias preserves exact grammar and evaluator public behavior', async () => {
  const body = 'preCLOSES #9; fixes\n#10; resolves\t#9; closes#11; closes owner/repo#12';
  assert.deepEqual(closingIssueNumbers(body), [9, 10, 9]);
  assert.deepEqual(closingIssueNumbers(null), []);
  const f = fake({ body, head: 'c'.repeat(40) });
  const results = await evaluateIssueValidation(f.github, context());
  assert.deepEqual(results, [{ headSha: 'c'.repeat(40), reference: { conclusion: 'success', summary: 'Validation passed.' }, approved: { conclusion: 'success', summary: 'Validation passed.' } }]);
  assert.deepEqual(f.requests, expected([9, 10, 9]));
});
