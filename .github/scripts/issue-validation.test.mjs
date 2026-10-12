import test from 'node:test';
import assert from 'node:assert/strict';
import { evaluateIssueValidation } from './issue-validation.mjs';

const repo = { owner: 'owner', repo: 'repo' };
const sha = 'a'.repeat(40);
const pull = (number = 1, body = 'Closes #9', head = sha) => ({ number, body, state: 'open', head: { sha: head } });
const prEvent = (action = 'opened') => ({ repo, eventName: 'pull_request_target', payload: { action, pull_request: { number: 1, body: 'Closes #999', head: { sha: 'old' } } } });
const issueEvent = (action = 'labeled') => ({ repo, eventName: 'issues', payload: { action, label: { name: 'status:approved' }, issue: { number: 9 } } });

function fake({ pulls = [pull()], issues = { 9: { labels: [{ name: 'status:approved' }] } }, change, fail, pages, queue } = {}) {
  const journal = [];
  const counts = {};
  const initial = structuredClone({ pulls, issues });
  function request(method, params) {
    journal.push([method, params]);
    counts[method] = (counts[method] || 0) + 1;
    if (fail?.(method, params, counts[method])) throw new Error('unavailable');
  }
  const github = { rest: {
    pulls: {
      get: async (params) => {
        request('pulls.get', params);
        const value = change?.(params.pull_number, counts['pulls.get']) ?? pulls.find((p) => p.number === params.pull_number);
        return { data: structuredClone(value) };
      },
      list: async (params) => {
        request('pulls.list', params);
        return { data: structuredClone(pages ? pages[params.page - 1] : pulls) };
      },
    },
    issues: { get: async (params) => { request('issues.get', params); return { data: structuredClone(issues[params.issue_number]) }; } },
    checks: { create: () => assert.fail('check write prohibited'), update: () => assert.fail('check write prohibited') },
  }, graphql: async (_query, params) => {
    request('graphql', params);
    return { repository: { mergeQueue: { entries: { nodes: queue, pageInfo: { hasNextPage: false } } } } };
  } };
  return { github, journal, unchanged: () => assert.deepEqual({ pulls, issues }, initial) };
}
const get = (n = 1) => ['pulls.get', { ...repo, pull_number: n }];
const issueGet = (n = 9) => ['issues.get', { ...repo, issue_number: n }];
const list = (page = 1) => ['pulls.list', { ...repo, state: 'open', per_page: 100, page }];
function conclusions(results, head, reference, approved) {
  assert.equal(results.length, 1);
  assert.equal(results[0].headSha, head);
  assert.equal(results[0].reference.conclusion, reference);
  assert.equal(results[0].approved.conclusion, approved);
  assert.equal(typeof results[0].reference.summary, 'string');
  assert.equal(typeof results[0].approved.summary, 'string');
}

for (const action of ['opened', 'edited', 'labeled', 'unlabeled', 'synchronize', 'reopened']) {
  test(`PR ${action} reads current metadata, not event body/head`, async () => {
    const f = fake();
    conclusions(await evaluateIssueValidation(f.github, prEvent(action)), sha, 'success', 'success');
    assert.deepEqual(f.journal, [get(), issueGet(), get()]);
    f.unchanged();
  });
}
for (const action of ['labeled', 'unlabeled']) {
  for (const approved of [true, false]) {
    test(`${action} uses current approval ${approved}, not event ordering`, async () => {
      const f = fake({ issues: { 9: { labels: approved ? [{ name: 'status:approved' }] : [] } } });
      conclusions(await evaluateIssueValidation(f.github, issueEvent(action)), sha, 'success', approved ? 'success' : 'failure');
      assert.deepEqual(f.journal, [list(), get(), issueGet(), get()]);
      f.unchanged();
    });
  }
}
test('irrelevant or invalid events perform no requests or writes', async () => {
  const cases = [
    { ...issueEvent(), payload: { ...issueEvent().payload, label: { name: 'type:bug' } } },
    { ...issueEvent(), payload: { ...issueEvent().payload, issue: { number: 9, pull_request: {} } } },
    issueEvent('edited'), prEvent('closed'), { ...prEvent(), eventName: 'push' },
    { ...prEvent(), payload: { action: 'opened', pull_request: { number: '1' } } },
  ];
  for (const context of cases) {
    const f = fake();
    assert.deepEqual(await evaluateIssueValidation(f.github, context), []);
    assert.deepEqual(f.journal, []);
    f.unchanged();
  }
});
test('exact legacy parser and all references, including duplicates', async () => {
  const f = fake({ pulls: [pull(1, 'preCLOSES #9; fixes\n#10; resolves #9; closes#11; closes owner/repo#12')], issues: { 9: { labels: [{ name: 'status:approved' }] }, 10: { labels: [] } } });
  conclusions(await evaluateIssueValidation(f.github, prEvent()), sha, 'success', 'failure');
  assert.deepEqual(f.journal, [get(), issueGet(), issueGet(10), issueGet(), get()]);
});
test('ordinary PR without references fails both checks', async () => {
  const f = fake({ pulls: [pull(1, 'mentions #9')] });
  conclusions(await evaluateIssueValidation(f.github, prEvent()), sha, 'failure', 'failure');
  assert.deepEqual(f.journal, [get(), get()]);
});
test('issue routing paginates and ignores stale listing bodies', async () => {
  const first = Array.from({ length: 100 }, (_, i) => pull(i + 1, 'Closes #9'));
  const current = [...first.map((p) => pull(p.number, 'mentions #9')), pull(101)];
  const f = fake({ pulls: current, pages: [first, [pull(101, 'no ref in listing')]] });
  conclusions(await evaluateIssueValidation(f.github, issueEvent()), sha, 'success', 'success');
  assert.deepEqual(f.journal, [list(), list(2), ...current.map((p) => get(p.number)), issueGet(), get(101)]);
});
for (const changed of [pull(1, 'Closes #9', 'b'.repeat(40)), pull(1, 'Closes #10'), { ...pull(), state: 'closed' }]) {
  test(`metadata recheck prevents stale success: ${JSON.stringify(changed)}`, async () => {
    const f = fake({ change: (_n, count) => count === 2 ? changed : pull() });
    const result = await evaluateIssueValidation(f.github, prEvent());
    conclusions(result, sha, 'failure', 'failure');
    assert.match(result[0].approved.summary, /metadata changed/);
    assert.deepEqual(f.journal, [get(), issueGet(), get()]);
  });
}
test('legacy approval accepts issues.get records with pull_request and approved labels', async () => {
  const f = fake({ issues: { 9: { labels: [{ name: 'status:approved' }], pull_request: {} } } });
  conclusions(await evaluateIssueValidation(f.github, prEvent()), sha, 'success', 'success');
  assert.deepEqual(f.journal, [get(), issueGet(), get()]);
  f.unchanged();
});
for (const value of [undefined, { labels: null }, { labels: [{ name: 4 }]}]) {
  test(`unavailable/malformed issue cannot approve: ${JSON.stringify(value)}`, async () => {
    const f = fake({ issues: { 9: value } });
    conclusions(await evaluateIssueValidation(f.github, prEvent()), sha, 'success', 'failure');
    assert.deepEqual(f.journal, [get(), issueGet(), get()]);
  });
}
for (const method of ['issues.get', 'pulls.get']) {
  test(`API failure ${method} is explicit and fail closed`, async () => {
    const f = fake({ fail: (m, _p, count) => m === method && (method !== 'pulls.get' || count === 2) });
    const result = await evaluateIssueValidation(f.github, prEvent());
    conclusions(result, sha, method === 'issues.get' ? 'success' : 'failure', 'failure');
    assert.match(result[0].approved.summary, /unavailable/);
    assert.deepEqual(f.journal, [get(), issueGet(), get()]);
  });
}
test('unresolvable initial target and discovery failures throw explicitly', async () => {
  for (const [context, method, expected] of [[prEvent(), 'pulls.get', [get()]], [issueEvent(), 'pulls.list', [list()]]]) {
    const f = fake({ fail: (m) => m === method });
    await assert.rejects(evaluateIssueValidation(f.github, context), /unavailable/);
    assert.deepEqual(f.journal, expected);
  }
});
test('known current SHA with malformed PR body fails closed', async () => {
  const f = fake({ pulls: [{ ...pull(), body: 123 }] });
  conclusions(await evaluateIssueValidation(f.github, prEvent()), sha, 'failure', 'failure');
  assert.deepEqual(f.journal, [get()]);
});
test('issue event without authoritative matching closing ref produces no target', async () => {
  const f = fake({ pulls: [pull(1, 'mentions #9')] });
  assert.deepEqual(await evaluateIssueValidation(f.github, issueEvent()), []);
  assert.deepEqual(f.journal, [list(), get()]);
  f.unchanged();
});
test('closed PR does not evaluate issues', async () => {
  const f = fake({ pulls: [{ ...pull(), state: 'closed' }] });
  assert.deepEqual(await evaluateIssueValidation(f.github, prEvent()), []);
  assert.deepEqual(f.journal, [get()]);
});
test('unsupported merge group ignores wrong-identity and malformed metadata without requests or writes', async () => {
  const f = fake({
    pulls: [pull(1, 'no closing reference'), pull(2)],
    queue: [1, 2].map((number) => ({ position: number, pullRequest: { number } })),
    change: () => pull(2),
  });
  const context = { repo, eventName: 'merge_group', payload: {
    action: 'checks_requested',
    merge_group: { base_ref: 'refs/heads/main', head_ref: `refs/heads/gh-readonly-queue/main/pr-2-${sha}`, head_sha: 'c'.repeat(40) },
  } };
  assert.deepEqual(await evaluateIssueValidation(f.github, context), []);
  assert.deepEqual(f.journal, []);
  f.unchanged();
  context.payload.merge_group = { head_sha: null };
  assert.deepEqual(await evaluateIssueValidation(f.github, context), []);
  assert.deepEqual(f.journal, []);
  f.unchanged();
});
