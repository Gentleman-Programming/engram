import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

// Exercise the complete checked-in github-script step, not a copied validator.
const workflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
const step = workflow.slice(workflow.indexOf('      - name: Verify linked issue is approved'));
const script = step.slice(step.indexOf('          script: |') + '          script: |'.length)
  .split(/\r?\n/).filter((line) => line.startsWith('            '))
  .map((line) => line.slice(12)).join('\n');
assert.ok(script.includes('core.setFailed'), 'expected complete issue-readiness step');
const execute = new Function('github', 'context', 'core', `return (async () => {${script}\n})();`);

const issue = (title, labels = ['status:approved'], assignees = [{ login: 'owner-not-pr-author' }]) => ({
  title, labels: labels.map((name) => ({ name })), assignees,
});
const missingOwner = (number, title) => `issue #${number} ("${title}") has no assignee. Assign an owner before merge.`;
const missingApproval = (number, title) => `issue #${number} ("${title}") does not have the \`status:approved\` label.`;

for (const scenario of [
  { name: 'accepts an approved issue assigned to someone other than the PR author', body: 'Closes #1', issues: { 1: issue('Ready') }, errors: [], fetched: [1] },
  { name: 'rejects partially malformed labels even with approval and an owner', body: 'Closes #1', issues: { 1: { ...issue('Ready'), labels: [{ name: 'status:approved' }, {}] } }, errors: ['issue #1: could not be fetched (issue labels are malformed)'], fetched: [1] },
  { name: 'rejects an approved unassigned issue', body: 'Fixes #2', issues: { 2: issue('Ready', undefined, []) }, errors: [missingOwner(2, 'Ready')], fetched: [2] },
  { name: 'preserves rejection of an unapproved assigned issue', body: 'Resolves #3', issues: { 3: issue('Waiting', []) }, errors: [missingApproval(3, 'Waiting')], fetched: [3] },
  { name: 'fails closed on API errors and still checks remaining issues', body: 'Closes #4, fixes #1', issues: { 1: issue('Ready') }, failAt: 4, errors: ['issue #4: could not be fetched (API unavailable)'], fetched: [4, 1] },
  { name: 'checks every linked issue and accumulates failures', body: 'Closes #5, fixes #6, resolves #7', issues: { 5: issue('Waiting', [], []), 6: issue('Ready', undefined, []), 7: issue('Done') }, errors: [missingApproval(5, 'Waiting'), missingOwner(5, 'Waiting'), missingOwner(6, 'Ready')], fetched: [5, 6, 7] },
  { name: 'preserves rejection of a missing closing reference', body: '', issues: {}, errors: ['no issue reference found'], fetched: [] },
  { name: 'does not grandfather an older PR with an unassigned issue', body: 'Closes #2', createdAt: '2020-01-01T00:00:00Z', issues: { 2: issue('Ready', undefined, []) }, errors: [missingOwner(2, 'Ready')], fetched: [2] },
]) test(scenario.name, async () => {
  const calls = [];
  const failures = [];
  const logs = [];
  const originalIssues = structuredClone(scenario.issues);
  await execute({ rest: { issues: { get: async (params) => {
    calls.push(params);
    if (params.issue_number === scenario.failAt) throw new Error('API unavailable');
    return { data: scenario.issues[params.issue_number] };
  } } } }, {
    eventName: 'pull_request', repo: { owner: 'owner', repo: 'repo' },
    payload: { pull_request: { number: 99, body: scenario.body, user: { login: 'author' }, created_at: scenario.createdAt } },
  }, { info: (message) => logs.push(message), setFailed: (message) => failures.push(message) });

  assert.deepEqual(calls, scenario.fetched.map((issue_number) => ({ owner: 'owner', repo: 'repo', issue_number })));
  assert.deepEqual(failures, scenario.errors.length ? [
    '❌ Linked issue(s) not approved:\n' + scenario.errors.map((error) => `PR #99: ${error}`).join('\n'),
  ] : []);
  assert.deepEqual(logs, scenario.fetched.flatMap((number) => {
    const messages = [`Checking PR #99 issue #${number} for status:approved label...`];
    if (number !== scenario.failAt && scenario.issues[number].labels.every((label) => typeof label?.name === 'string') && scenario.issues[number].labels.some(({ name }) => name === 'status:approved')) {
      messages.push(`✅ PR #99 issue #${number} has status:approved`);
    }
    return messages;
  }));
  assert.deepEqual(scenario.issues, originalIssues, 'validation must not mutate issue metadata');
});
