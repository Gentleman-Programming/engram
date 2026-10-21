import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { references, autoAssign } from './issue-auto-assign.mjs';

function fixture() {
  const issue = () => ({ state: 'open', labels: [{ name: 'status:approved' }], assignees: [] });
  const f = { pr: { state: 'open', draft: false, user: { type: 'User', login: 'author' }, body: 'Closes #1' },
    stored: { 1: issue(), 2: issue() }, calls: [], reads: 0, summaries: [], failures: [] };
  f.context = { repo: { owner: 'local', repo: 'repo' }, payload: { pull_request: { number: 9, body: 'Closes #2' } }, actor: 'editor' };
  f.core = { summary: { addRaw(text) { f.summaries.push(text); return this; }, async write() {} },
    setFailed(text) { f.failures.push(text); } };
  f.github = { rest: { pulls: { async get(args) { f.calls.push(['pr', args]); if (f.prError) throw Error('private data'); return { data: f.pr }; } },
    issues: { async get(args) {
      f.calls.push(['get', args]); f.reads++;
      if (f.readError === f.reads) throw Error('private data');
      if (f.race && f.reads === 2) f.stored[1].assignees = [{ login: 'other' }];
      return { data: structuredClone(f.stored[args.issue_number]) };
    }, async addAssignees(args) {
      f.calls.push(['post', args]); if (f.postError) throw Error('private data');
      if (!f.silent) f.stored[args.issue_number].assignees.push({ login: args.assignees[0] });
      return { data: {} };
    } } } };
  f.run = () => autoAssign(f.github, f.context, f.core);
  f.posts = () => f.calls.filter(([kind]) => kind === 'post');
  return f;
}

test('closing references are local, bounded, safe and deduplicated', () => {
  assert.deepEqual(references('Closes #123; Fixes #456\nResolves #789 closes #123'), [123, 456, 789]);
  for (const body of ['#1', 'discloses #1', 'preFixes #1', 'Fixes owner/repo#1', 'Closes https://github.com/a/b/issues/1',
    'Closes #0', 'Closes #-1', 'Closes #01', 'Closes #9007199254740992', 'Closes #1abc', 'Closes #1.5', 'Closes #1/other']) {
    assert.deepEqual(references(body), [], body);
  }
  assert.deepEqual(references(null), []);
});

test('current API body and human author, not actor or stale payload; idempotent', async () => {
  const f = fixture(); await f.run(); await f.run();
  assert.equal(f.posts().length, 1);
  assert.deepEqual(f.posts()[0][1], { owner: 'local', repo: 'repo', issue_number: 1, assignees: ['author'] });
  assert.deepEqual(f.stored[2].assignees, []);
  assert.equal(f.failures.length, 0);
  assert.match(f.summaries.join(''), /Assigned/);
});

for (const [name, patch] of Object.entries({ draft: { draft: true }, closed: { state: 'closed' },
  bot: { user: { type: 'Bot', login: 'robot' } }, botLogin: { user: { type: 'User', login: 'robot[bot]' } },
  malformed: { user: null }, missingDraft: { draft: undefined } })) {
  test(`skip ${name} PR without issue reads or mutations`, async () => {
    const f = fixture(); Object.assign(f.pr, patch); const before = structuredClone(f.stored);
    await f.run(); assert.equal(f.reads, 0); assert.equal(f.posts().length, 0); assert.deepEqual(f.stored, before);
    assert.ok(f.summaries.length);
  });
}

for (const [name, patch] of Object.entries({ closed: { state: 'closed' }, unapproved: { labels: [] },
  pull: { pull_request: {} }, own: { assignees: [{ login: 'author' }] }, other: { assignees: [{ login: 'other' }] },
  malformedLabels: { labels: null }, malformedAssignees: { assignees: [{}] }, missingAssignees: { assignees: undefined } })) {
  test(`preserve ${name} issue`, async () => {
    const f = fixture(); Object.assign(f.stored[1], patch); const before = structuredClone(f.stored);
    await f.run(); assert.equal(f.posts().length, 0); assert.deepEqual(f.stored, before);
    assert.ok(f.summaries.length);
    if (name.startsWith('malformed') || name.startsWith('missing')) assert.equal(f.failures.length, 1);
  });
}

test('fresh pre-write read preserves racing manual ownership', async () => {
  const f = fixture(); f.race = true; await f.run();
  assert.equal(f.reads, 2); assert.equal(f.posts().length, 0);
  assert.deepEqual(f.stored[1].assignees, [{ login: 'other' }]);
});

test('duplicates cause one write and verified readback', async () => {
  const f = fixture(); f.pr.body = 'Closes #1 Fixes #1'; await f.run();
  assert.deepEqual(f.calls.map(([kind]) => kind), ['pr', 'get', 'get', 'post', 'get']);
});

for (const [name, patch] of Object.entries({ prGET: { prError: true }, initialGET: { readError: 1 },
  freshGET: { readError: 2 }, POST: { postError: true }, readbackGET: { readError: 3 }, silent: { silent: true } })) {
  test(`${name} failure is visible, never reports assignment`, async () => {
    const f = fixture(); Object.assign(f, patch); await f.run();
    assert.equal(f.failures.length, 1); assert.doesNotMatch(f.summaries.join(''), /Assigned/);
    assert.doesNotMatch(f.summaries.join('') + f.failures.join(''), /private data/);
    if (name !== 'readbackGET') assert.deepEqual(f.stored[1].assignees, []);
  });
}

test('partial progress remains visible and subsequent issues are attempted', async () => {
  const f = fixture(); f.pr.body = 'Closes #1 Fixes #2'; f.readError = 4;
  await f.run(); assert.deepEqual(f.stored[1].assignees, [{ login: 'author' }]);
  assert.deepEqual(f.stored[2].assignees, []); assert.equal(f.failures.length, 1);
  assert.match(f.summaries.join(''), /Assigned/); assert.match(f.summaries.join(''), /Failed/);
});

test('rejected references have no issue traffic or stored effects', async () => {
  const f = fixture(); f.pr.body = 'discloses #1 Fixes other/repo#2 #1 Closes #9007199254740992';
  const before = structuredClone(f.stored); await f.run();
  assert.equal(f.reads, 0); assert.equal(f.posts().length, 0); assert.deepEqual(f.stored, before);
});

test('failure on one issue does not suppress later eligible references', async () => {
  const f = fixture(); f.pr.body = 'Closes #1 Fixes #2'; f.readError = 1; await f.run();
  assert.equal(f.failures.length, 1); assert.deepEqual(f.stored[1].assignees, []);
  assert.deepEqual(f.stored[2].assignees, [{ login: 'author' }]);
});

test('summary failure fails automation; untrusted text is never rendered', async () => {
  const f = fixture(); f.pr.body = 'Closes #1 <script>evil</script>'; await f.run();
  assert.doesNotMatch(f.summaries.join(''), /script|evil|author/);
  f.core.summary.write = async () => { throw Error('summary failed'); };
  await f.run(); assert.equal(f.failures.length, 1);
});

test('workflow trust, permissions, serialization and executable github-script boundary', async () => {
  const yaml = readFileSync(new URL('../workflows/issue-auto-assign.yml', import.meta.url), 'utf8').replace(/\r\n/g, '\n');
  assert.match(yaml, /pull_request_target:\s*\n\s*types: \[opened, edited, reopened, ready_for_review\]/);
  assert.match(yaml, /group: issue-auto-assign\s*\n\s*cancel-in-progress: false/);
  assert.match(yaml, /permissions: \{\}/);
  assert.deepEqual([...yaml.matchAll(/^      ([\w-]+): (read|write)$/gm)].map(m => m.slice(1)),
    [['contents', 'read'], ['issues', 'write'], ['pull-requests', 'read']]);
  assert.match(yaml, /actions\/checkout@d23441a48e516b6c34aea4fa41551a30e30af803/);
  assert.match(yaml, /actions\/github-script@ed597411d8f924073f98dfc5c65a23a2325f34cd/);
  assert.match(yaml, /ref: \$\{\{ github.event.pull_request.base.sha \}\}/);
  assert.match(yaml, /persist-credentials: false/);
  assert.match(yaml, /sparse-checkout: \.github\/scripts\/issue-auto-assign.mjs/);
  assert.doesNotMatch(yaml, /head\.sha|workflow_sha|secrets\.|run:|checks:|actions: write/);
  const script = yaml.split('          script: |\n')[1].split('\n').map(line => line.replace(/^            /, '')).join('\n');
  const execute = new (Object.getPrototypeOf(async function() {}).constructor)('github', 'context', 'core', 'load',
    script.replace(/await import\([^\n]+\)/, 'await load()'));
  const f = fixture(); await execute(f.github, f.context, f.core, async () => ({ autoAssign }));
  assert.equal(f.posts().length, 1); assert.equal(f.failures.length, 0); assert.ok(f.summaries.length);
  await execute(f.github, f.context, f.core, async () => { throw Error('load failed'); });
  assert.equal(f.failures.length, 1);
});
