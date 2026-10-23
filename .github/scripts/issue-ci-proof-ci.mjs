import { createProofControl } from './issue-ci-proof-controls.mjs';
import { setTimeout as sleep } from 'node:timers/promises';

const fail = () => new Error('proof-ci: configuration');
const positive = (n) => Number.isSafeInteger(n) && n > 0;
const sha = (s) => typeof s === 'string' && /^[a-f0-9]{40}$/.test(s);
const query = `query CIRelease($owner: String!, $repo: String!, $issue: Int!) {
  repository(owner: $owner, name: $repo) { issue(number: $issue) {
    comments(last: 20) { nodes { body lastEditedAt author { __typename ... on User { databaseId } } } }
  } }
}`;

// Identity is captured by the workflow host BEFORE checking out candidate code.
export async function holdCI({ raw, identity: b, github, result, emit,
  now = Date.now, wait = sleep, nonce, timers } = {}) {
  if (raw === undefined || raw === '') return result;
  let cfg;
  try {
    if (typeof raw !== 'string' || raw.length > 8192) throw fail();
    cfg = JSON.parse(raw);
  } catch { throw fail(); }
  if (cfg?.enabled === false) return result;
  if (!['pull_request', 'merge_group'].includes(b?.event)) return result;
  if (cfg?.enabled !== true || !cfg.target || typeof cfg.target !== 'object') throw fail();
  if (!sha(b.head) || !sha(b.checkout) || !sha(b.workflow) || !positive(b.runId)
    || !positive(b.attempt) || b.job !== 'unit-tests'
    || !/^[\w.-]+\/[\w.-]+$/.test(b.repo)
    || !/^[\w-]+$/.test(b.action)
    || (b.event === 'pull_request' ? !sha(b.prHead) : b.prHead !== null)) throw fail();
  const optional = ['runId', 'attempt', 'workflow', 'head'];
  if (Object.keys(b).some((key) => (!optional.includes(key) || key in cfg.target)
    && cfg.target[key] !== b[key])) return result;
  if (!positive(cfg.issueNumber) || typeof cfg.case !== 'string'
    || !/^[\w.:-]{1,100}$/.test(cfg.case)) throw fail();
  const [owner, repo] = b.repo.split('/');
  let nextRead = 0;
  const readRelease = async (request) => {
    const delay = Math.min(nextRead - now(), request.expiresAt - now());
    if (delay > 0) await wait(delay);
    if (now() >= request.expiresAt) throw fail();
    const data = await github.graphql(query, { owner, repo, issue: cfg.issueNumber });
    nextRead = now() + 30_000;
    const nodes = data?.repository?.issue?.comments?.nodes;
    if (!Array.isArray(nodes) || nodes.length > 20) throw fail();
    for (const node of [...nodes].reverse()) {
      const actor = node?.author;
      if (node?.lastEditedAt !== null || actor?.__typename !== 'User'
        || !positive(actor.databaseId) || !cfg.actorIds.includes(actor.databaseId)
        || typeof node.body !== 'string' || node.body.length > 4096) continue;
      let value;
      try { value = JSON.parse(node.body); } catch { continue; }
      if (value && Object.keys(request).every((key) => value[key] === request[key])) {
        return { ...request, actorId: actor.databaseId };
      }
    }
    return null;
  };
  return createProofControl({ manifest: cfg, binding: {
    case: `${b.repo}:${cfg.case}`, runId: b.runId, attempt: b.attempt,
    phase: `ci:${b.job}:${b.event}:${b.action}:${b.prHead ?? 'group'}`,
    head: b.head, source: `${b.workflow}/${b.checkout}`,
  }, readRelease, now, wait, emit, nonce, timers }).hold(result);
}
