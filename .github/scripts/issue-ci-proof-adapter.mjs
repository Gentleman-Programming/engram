import { setTimeout as sleep } from 'node:timers/promises';
import { createProofControl } from './issue-ci-proof-controls.mjs';

const configuration = () => new Error('proof-adapter: configuration');
const positive = (n) => Number.isSafeInteger(n) && n > 0;
const sha = (s) => typeof s === 'string' && /^[a-f0-9]{40}$/.test(s);
const query = `query ProofRelease($owner: String!, $repo: String!, $issue: Int!) {
  repository(owner: $owner, name: $repo) { issue(number: $issue) {
    comments(last: 20) { nodes { body lastEditedAt author { __typename ... on User { databaseId } } } }
  } }
}`;
const layer = (object, key, value) => Object.defineProperty(Object.create(object), key, { value });

// Runtime/context must come from the trusted Actions host, never PR text or release JSON.
export function createGitHubProof({ github, context, raw, phase, checkout, runtime, environment = process.env,
  emit, now = Date.now, wait = sleep, nonce, timers,
}) {
  const disabled = Object.freeze({ client: github, complete: async (value) => value, assert: () => {} });
  if (raw === undefined || raw === '') return disabled;
  let cfg;
  try {
    if (typeof raw !== 'string' || raw.length > 8192) throw configuration();
    cfg = JSON.parse(raw);
  } catch { throw configuration(); }
  if (cfg?.enabled === false) return disabled;
  if (cfg?.enabled !== true || !cfg.target || typeof cfg.target !== 'object' || Array.isArray(cfg.target)
    || !positive(cfg.issueNumber)
    || typeof cfg.case !== 'string' || !/^[a-zA-Z0-9_.:/@-]{1,120}$/.test(cfg.case)) throw configuration();
  const native = phase === 'native-read:reference' || phase === 'native-read:approved';
  const legacy = phase === 'legacy-completion:reference' || phase === 'legacy-completion:approved';
  if (!native && !legacy) throw configuration();
  if (context?.eventName !== (native ? 'pull_request_target' : 'pull_request')) return disabled;
  runtime ??= { runId: Number(environment.GITHUB_RUN_ID), attempt: Number(environment.GITHUB_RUN_ATTEMPT),
    workflow: environment.GITHUB_WORKFLOW_SHA };
  const repo = { owner: context?.repo?.owner, repo: context?.repo?.repo };
  const head = context?.payload?.pull_request?.head?.sha;
  if (!sha(head) || !sha(checkout) || !sha(runtime.workflow)
    || !positive(runtime.runId) || !positive(runtime.attempt)
    || ![repo.owner, repo.repo].every((s) => typeof s === 'string' && /^[\w.-]+$/.test(s))) {
    throw configuration();
  }
  // Bind BOTH workflow definition and trusted checked-out implementation revisions.
  const target = { repo: `${repo.owner}/${repo.repo}`, runId: runtime.runId,
    attempt: runtime.attempt, phase, head, source: `${runtime.workflow}/${checkout}` };
  const selector = { repo: target.repo, prNumber: context.payload.pull_request.number,
    action: context.payload.action, checkout, phase, head };
  if (!positive(selector.prNumber) || !Object.keys(selector).every((key) => cfg.target[key] === selector[key])
    || ['runId', 'attempt', 'source'].some((key) => key in cfg.target && cfg.target[key] !== target[key])) return disabled;
  const binding = { case: `${target.repo}:${cfg.case}`, runId: target.runId,
    attempt: target.attempt, phase, head, source: target.source };
  let nextRead = 0;
  const readRelease = async (request) => {
    // Space real API reads, not the evaluator witness. The foundation deadline still wins.
    const delay = Math.min(nextRead - now(), request.expiresAt - now());
    if (delay > 0) await wait(delay);
    if (now() >= request.expiresAt) throw configuration();
    const data = await github.graphql(query, { ...repo, issue: cfg.issueNumber });
    nextRead = now() + 30_000;
    const nodes = data?.repository?.issue?.comments?.nodes;
    if (!Array.isArray(nodes) || nodes.length > 20) throw configuration();
    for (const node of [...nodes].reverse()) {
      // An editor must not inherit an allowlisted original author's identity.
      const actor = node?.author;
      if (node?.lastEditedAt !== null || actor?.__typename !== 'User'
        || !positive(actor.databaseId) || !cfg.actorIds.includes(actor.databaseId)
        || typeof node.body !== 'string' || node.body.length > 4096) continue;
      let value;
      try { value = JSON.parse(node.body); } catch { continue; }
      if (value && Object.keys(request).every((key) => value[key] === request[key])) {
        return { ...request, actorId: actor.databaseId }; // Never trust body.actorId.
      }
    }
    return null;
  };
  const control = createProofControl({ manifest: cfg, binding, readRelease, emit, now, wait, nonce, timers });
  let pending, fault;
  const readIssue = async (...args) => {
    const response = await github.rest.issues.get(...args);
    // First real read has returned, but the existing evaluator has NOT consumed it.
    pending ??= control.hold(response);
    try { await pending; } catch (error) { fault = error; throw error; }
    return response;
  };
  return Object.freeze({
    client: native ? layer(github, 'rest', layer(github.rest, 'issues',
      layer(github.rest.issues, 'get', readIssue))) : github,
    complete: legacy ? (value) => control.hold(value) : async (value) => value,
    // Existing validation catches issue-read errors; do not let that mask a failed hold.
    assert: () => { if (fault) throw fault; },
  });
}
