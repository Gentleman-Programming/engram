import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const workflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
const start = workflow.indexOf('      - name: Verify linked issue is approved');
const step = workflow.slice(start).match(/[\s\S]*?(?=^  [a-z-]+:|(?![\s\S]))/m)?.[0];
const code = step.match(/            const issuePattern = [\s\S]*?^            };/m)?.[0]
  .replace(/^            /gm, '');
assert.ok(code, 'expected inline issue-readiness validator');

function fixture(issues, failure) {
  const calls = [];
  const github = { rest: { issues: { get: async (params) => {
    calls.push(params);
    if (failure) throw new Error(failure);
    return { data: issues[params.issue_number] };
  } } } };
  const validate = new Function('github', 'context', 'core', `${code}\nreturn validate;`)(
    github, { repo: { owner: 'owner', repo: 'repo' } }, { info() {} },
  );
  return { calls, validate };
}

const issue = (title, labels = ['status:approved'], assignees = [{}]) => ({
  title, labels: labels.map((name) => ({ name })), assignees,
});

for (const { name, body, issues, failure, expected, fetched } of [
  { name: 'accepts an approved assigned issue', body: 'Closes #1', issues: { 1: issue('Ready') }, expected: [], fetched: [1] },
  { name: 'rejects an approved unassigned issue', body: 'Fixes #2', issues: { 2: issue('Ready', undefined, []) }, expected: ['issue #2 ("Ready") has no assignee. Assign an owner before implementation.'], fetched: [2] },
  { name: 'rejects an unapproved assigned issue', body: 'Resolves #3', issues: { 3: issue('Waiting', [], [{}]) }, expected: ['issue #3 ("Waiting") does not have the `status:approved` label.'], fetched: [3] },
  { name: 'fails closed when an issue API lookup fails', body: 'Closes #4', issues: {}, failure: 'API unavailable', expected: ['issue #4: could not be fetched (API unavailable)'], fetched: [4] },
  { name: 'fetches every linked issue and accumulates errors', body: 'Closes #5, fixes #6, resolves #7', issues: { 5: issue('Waiting', [], []), 6: issue('Ready', undefined, []), 7: issue('Done') }, expected: ['issue #5 ("Waiting") does not have the `status:approved` label.', 'issue #5 ("Waiting") has no assignee. Assign an owner before implementation.', 'issue #6 ("Ready") has no assignee. Assign an owner before implementation.'], fetched: [5, 6, 7] },
]) test(name, async () => {
  const current = fixture(issues, failure);
  assert.deepEqual(await current.validate({ number: 99, body }), expected);
  assert.deepEqual(current.calls.map(({ issue_number }) => issue_number), fetched);
});
