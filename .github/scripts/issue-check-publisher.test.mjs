import test from 'node:test';
import assert from 'node:assert/strict';

const repo = { owner: 'Gentleman-Programming', repo: 'engram' };
const headSha = 'a'.repeat(40);
const names = ['Check Issue Reference', 'Check Issue Has status:approved'];
const evaluation = () => ({ headSha, reference: { conclusion: 'success', summary: 'Closes #1802' }, approved: { conclusion: 'failure', summary: 'Issue #1802 not approved' } });
const publish = async (...args) => (await import('./issue-check-publisher.mjs')).publishIssueChecks(...args);
function fake(transform = (data) => data) {
  const journal = [];
  const github = { rest: { checks: { create: async (request) => {
    journal.push(structuredClone(request));
    return { data: transform({ ...request, id: journal.length, app: { id: 15368 } }) };
  } } } };
  return { github, journal };
}

test('publishes only two exact completed names, explicit targets, summaries and conclusions', async () => {
  const { github, journal } = fake();
  const second = { ...evaluation(), headSha: 'b'.repeat(40) };
  await publish(github, repo, [evaluation(), second]);
  assert.deepEqual(journal, [evaluation(), second].flatMap((item) => names.map((name, index) => ({
    ...repo, name, head_sha: item.headSha, status: 'completed',
    conclusion: index === 0 ? item.reference.conclusion : item.approved.conclusion,
    output: { title: name, summary: index === 0 ? item.reference.summary : item.approved.summary },
  }))));
});

test('empty batch makes no requests', async () => {
  const { github, journal } = fake();
  await publish(github, repo, []);
  assert.deepEqual(journal, []);
});

for (const [rule, mutate] of [
  ['explicit full SHA', (item) => { item.headSha = 'main'; }],
  ['required head', (item) => { delete item.headSha; }],
  ['only success/failure', (item) => { item.approved.conclusion = 'neutral'; }],
  ['required summary', (item) => { delete item.reference.summary; }],
  ['nonempty summary', (item) => { item.reference.summary = ' '; }],
  ['required result', (item) => { item.approved = null; }],
  ['unknown fields', (item) => { item.name = 'Other gate'; }],
  ['unknown output fields', (item) => { item.reference.title = 'Other gate'; }],
]) {
  test(`validates entire batch before writing: ${rule}`, async () => {
    const { github, journal } = fake();
    const invalid = evaluation();
    mutate(invalid);
    await assert.rejects(publish(github, repo, [evaluation(), invalid]), /Invalid/);
    assert.deepEqual(journal, []);
  });
}

for (const invalid of [null, {}, 'unrelated']) {
  test(`rejects unrelated batch ${JSON.stringify(invalid)} without requests`, async () => {
    const { github, journal } = fake();
    await assert.rejects(publish(github, repo, invalid), /Invalid/);
    assert.deepEqual(journal, []);
  });
}

test('validates repository before writes', async () => {
  const { github, journal } = fake();
  await assert.rejects(publish(github, { owner: '', repo: 'engram' }, [evaluation()]), /Invalid/);
  assert.deepEqual(journal, []);
});

for (const [rule, transform] of [
  ['wrong provider', (data) => ({ ...data, app: { id: 42 } })],
  ['missing provider', (data) => ({ ...data, app: undefined })],
  ['wrong head', (data) => ({ ...data, head_sha: 'b'.repeat(40) })],
  ['wrong name', (data) => ({ ...data, name: 'Other gate' })],
  ['wrong conclusion', (data) => ({ ...data, conclusion: 'neutral' })],
  ['not completed', (data) => ({ ...data, status: 'queued' })],
]) {
  test(`explicit blocker stops remaining publication: ${rule}`, async () => {
    const { github, journal } = fake(transform);
    await assert.rejects(publish(github, repo, [evaluation()]), /blocker/i);
    assert.equal(journal.length, 1);
  });
}

test('capability denial propagates and never seeks other credentials or APIs', async () => {
  const { github, journal } = fake(() => { throw Object.assign(new Error('Resource not accessible'), { status: 403 }); });
  await assert.rejects(publish(github, repo, [evaluation()]), /blocker.*Resource not accessible/i);
  assert.equal(journal.length, 1);
});

test('missing Checks capability is explicit and makes no writes', async () => {
  await assert.rejects(publish({ rest: {} }, repo, [evaluation()]), /blocker/i);
});

test('repeated reconciliation creates evidence without touching legacy checks', async () => {
  const { github, journal } = fake();
  const approved = evaluation();
  approved.approved.conclusion = 'success';
  await publish(github, repo, [approved]);
  await publish(github, repo, [evaluation()]);
  assert.deepEqual(journal.map(({ name, head_sha, conclusion }) => ({ name, head_sha, conclusion })), [
    { name: names[0], head_sha: headSha, conclusion: 'success' },
    { name: names[1], head_sha: headSha, conclusion: 'success' },
    { name: names[0], head_sha: headSha, conclusion: 'success' },
    { name: names[1], head_sha: headSha, conclusion: 'failure' },
  ]);
});

test('snapshots every target and result before awaiting the first write', async () => {
  const input = [evaluation()];
  const mutableRepo = { ...repo };
  const { github, journal } = fake((data) => {
    input[0].headSha = 'b'.repeat(40);
    input[0].approved.conclusion = 'success';
    mutableRepo.repo = 'other';
    return data;
  });
  await publish(github, mutableRepo, input);
  assert.ok(journal.every((request) => request.head_sha === headSha && request.repo === repo.repo));
  assert.equal(journal[1].conclusion, 'failure');
});

test('later API failure leaves partial publication explicit and stops the batch', async () => {
  let attempts = 0;
  const { github, journal } = fake((data) => {
    if (++attempts === 2) throw new Error('API unavailable');
    return data;
  });
  await assert.rejects(publish(github, repo, [evaluation(), evaluation()]), /blocker.*API unavailable/i);
  assert.equal(journal.length, 2);
});
