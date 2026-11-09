import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

const workflow = () => fs.readFileSync('.github/workflows/issue-status-fallback.yml', 'utf8');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;

// Execute the actual github-script body, not a separate implementation of its policy.
async function runWorkflow(currentLabels, { eventLabels = [], body = null, eventBody = null, readError, writeError } = {}) {
  const source = workflow();
  const script = source.match(/          script: \|\r?\n([\s\S]+)$/)?.[1];
  assert.ok(script, 'workflow contains an executable github-script body');
  const calls = [];
  const originalLabels = structuredClone(currentLabels);
  const context = {
    repo: { owner: 'example', repo: 'engram' },
    issue: { number: 42 },
    payload: { issue: { body: eventBody, title: 'Bug feature documentation question', labels: eventLabels.map((name) => ({ name })) } },
  };
  const github = {
    rest: {
      issues: {
        async get(args) {
          calls.push(['get', args]);
          if (readError) throw readError;
          return { data: { body, labels: currentLabels.map((name) => ({ name })) } };
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

// Required field labels are metadata, not a classification of the answers.
const signatures = {
  bug: ['Existing Issue Search', '📝 Observed Consequence', '🔄 Steps to Reproduce', '✅ Expected Behavior', '❌ Actual Behavior', 'Operating System', 'Release Version (Report Context)', 'Agent / Client'],
  feature: ['Existing Issue Search', '🔍 Observed Consequence', '💡 Desired Outcome', '📦 Affected Area', 'Release Context'],
  docs: ['Existing Issue Search', '📄 Documentation Reference', '📄 Which Document?', '🔍 Observed Consequence', 'Release Context'],
  question: ['Existing Issue Search', 'Question', 'Why does this need issue tracking?', 'Affected Area', 'Release Context'],
};
const formBody = (type) => signatures[type].map((heading) => `### ${heading}\n\nAnswer`).join('\n\n');

async function expectPolicy(initial, body, additions, options = {}) {
  const result = await runWorkflow([...initial], { body, ...options });
  assert.equal(result.error, options.writeError ?? options.readError);
  assert.deepEqual(result.calls, [
    ['get', issue],
    ...(!options.readError && additions.length ? [['addLabels', { ...issue, labels: additions }]] : []),
  ]);
  assert.deepEqual(result.labels, options.writeError || options.readError ? initial : [...initial, ...additions]);
  assert.deepEqual(result.originalLabels, initial);
}

test('recovers each current form type independently of status and preserves labels', async () => {
  for (const type of Object.keys(signatures)) {
    for (const status of [[], ['status:approved']]) {
      await expectPolicy([...status, 'help wanted'], formBody(type), [...(status.length ? [] : ['status:needs-review']), `type:${type}`]);
    }
    await expectPolicy(['type:custom', 'priority:high'], formBody(type), ['status:needs-review']);
    await expectPolicy(['type:custom', 'status:blocked'], formBody(type), []);
  }
});

test('uses only current body and accepts ordinary Markdown line endings', async () => {
  await expectPolicy(['status:approved'], formBody('docs').replaceAll('\n', '\r\n'), ['type:docs'], { eventBody: formBody('bug') });
  await expectPolicy(['status:approved'], null, [], { eventBody: formBody('bug') });
});

test('unknown incomplete ambiguous and fenced structures do not infer types', async () => {
  for (const body of [null, '', 'bug documentation feature question', '### Question', formBody('bug') + '\n' + formBody('docs'), '```markdown\n' + formBody('bug') + '\n```', '~~~~markdown\n' + formBody('docs') + '\n~~~~']) {
    await expectPolicy([], body, ['status:needs-review']);
  }
  for (const type of Object.keys(signatures)) {
    for (const missing of signatures[type]) {
      await expectPolicy(['status:approved'], formBody(type).replace(`### ${missing}\n`, ''), []);
    }
  }
  await expectPolicy(['status:approved'], formBody('bug') + '\n```\n' + formBody('docs') + '\n```', ['type:bug']);
});

test('type recovery API failures preserve input and propagate', async () => {
  await expectPolicy(['status:approved'], formBody('bug'), [], { readError: new Error('read denied') });
  await expectPolicy(['status:approved'], formBody('bug'), ['type:bug'], { writeError: new Error('write denied') });
});

test('signatures remain aligned with required fields of current forms', () => {
  const files = { bug: 'bug_report', feature: 'feature_request', docs: 'docs_improvement', question: 'tracked_question' };
  for (const [type, file] of Object.entries(files)) {
    const source = fs.readFileSync(`.github/ISSUE_TEMPLATE/${file}.yml`, 'utf8');
    const required = source.split(/\r?\n  - type:/).slice(1).filter((block) => /required: true/.test(block)).map((block) => block.match(/      label: (.+)/)[1].replace(/^"|"$/g, ''));
    assert.deepEqual(signatures[type], required);
  }
});

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
