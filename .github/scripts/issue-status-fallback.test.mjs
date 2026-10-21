import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const workflow = () => fs.readFileSync('.github/workflows/issue-status-fallback.yml', 'utf8');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;

// Execute the actual github-script body, not a separate implementation of its policy.
async function runWorkflow(currentLabels, { eventLabels = [], readError, writeError } = {}) {
  const source = workflow();
  const script = source.match(/          script: \|\r?\n([\s\S]+)$/)?.[1];
  assert.ok(script, 'workflow contains an executable github-script body');
  const calls = [];
  const originalLabels = structuredClone(currentLabels);
  const context = {
    repo: { owner: 'example', repo: 'engram' },
    issue: { number: 42 },
    payload: { issue: { labels: eventLabels.map((name) => ({ name })) } },
  };
  const github = {
    rest: {
      issues: {
        async get(args) {
          calls.push(['get', args]);
          if (readError) throw readError;
          return { data: { labels: currentLabels.map((name) => ({ name })) } };
        },
        async addLabels(args) {
          calls.push(['addLabels', args]);
          if (writeError) throw writeError;
          currentLabels.push(...args.labels.filter((name) => !currentLabels.includes(name)));
        },
      },
    },
  };
  let error;
  try {
    await new AsyncFunction('github', 'context', script)(github, context);
  } catch (caught) {
    error = caught;
  }
  return { calls, error, labels: currentLabels, originalLabels };
}

const issue = { owner: 'example', repo: 'engram', issue_number: 42 };

test('adds only the review status when current labels have no status', async () => {
  for (const initial of [[], ['type:bug', 'priority:high', 'help wanted']]) {
    const result = await runWorkflow([...initial]);
    assert.equal(result.error, undefined);
    assert.deepEqual(result.calls, [
      ['get', issue],
      ['addLabels', { ...issue, labels: ['status:needs-review'] }],
    ]);
    assert.deepEqual(result.labels, [...initial, 'status:needs-review']);
  }
});

test('preserves every existing status and unrelated label without any write', async () => {
  for (const status of ['status:needs-review', 'status:approved', 'status:blocked', 'status:custom']) {
    const initial = ['type:feature', status, 'effort:small'];
    const result = await runWorkflow([...initial]);
    assert.equal(result.error, undefined);
    assert.deepEqual(result.calls, [['get', issue]]);
    assert.deepEqual(result.labels, initial);
  }
});

test('uses current API labels rather than the opened event snapshot', async () => {
  const approved = await runWorkflow(['status:approved'], { eventLabels: [] });
  assert.equal(approved.error, undefined);
  assert.deepEqual(approved.calls, [['get', issue]]);
  assert.deepEqual(approved.labels, ['status:approved']);

  const missing = await runWorkflow(['type:docs'], { eventLabels: ['status:needs-review'] });
  assert.equal(missing.error, undefined);
  assert.deepEqual(missing.calls, [
    ['get', issue],
    ['addLabels', { ...issue, labels: ['status:needs-review'] }],
  ]);
  assert.deepEqual(missing.labels, ['type:docs', 'status:needs-review']);
});

test('read failures propagate without a label write', async () => {
  const failure = new Error('API read denied');
  const result = await runWorkflow(['type:bug'], { readError: failure });
  assert.equal(result.error, failure);
  assert.deepEqual(result.calls, [['get', issue]]);
  assert.deepEqual(result.labels, result.originalLabels);
});

test('write failures propagate without retries or replacement', async () => {
  const failure = new Error('API write denied');
  const result = await runWorkflow(['type:bug'], { writeError: failure });
  assert.equal(result.error, failure);
  assert.deepEqual(result.calls, [
    ['get', issue],
    ['addLabels', { ...issue, labels: ['status:needs-review'] }],
  ]);
  assert.deepEqual(result.labels, result.originalLabels);
});

test('workflow only handles issue opens with minimal permissions and no checkout', () => {
  const source = workflow();
  assert.match(source, /^on:\r?\n  issues:\r?\n    types: \[opened\]\r?\n\r?\npermissions:/m);
  assert.match(source, /^permissions:\r?\n  issues: write\r?\n\r?\njobs:/m);
  assert.equal((source.match(/uses:/g) ?? []).length, 1);
  assert.match(source, /uses: actions\/github-script@[a-f0-9]{40} # v8/);
  assert.doesNotMatch(source, /\$\{\{|checkout@|run:|pull_request_target:|workflow_dispatch:/);
});
