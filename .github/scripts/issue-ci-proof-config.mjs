import { createHash } from 'node:crypto';

const fail = (code) => new Error(`proof-config: ${code}`);
const positive = (n) => Number.isSafeInteger(n) && n > 0;
const sha = (s) => typeof s === 'string' && /^[a-f0-9]{40}$/.test(s);
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const query = `query CIConfiguration($owner: String!, $repo: String!, $issue: Int!) {
  repository(owner: $owner, name: $repo) { issue(number: $issue) {
    comments(last: 20) { nodes { body lastEditedAt author { __typename ... on User { databaseId } } } }
  } }
}`;

// Routing is untrusted. Authority comes from current canonical policy DATA,
// independently of the immutable event-base revision supplying executable code.
export async function readCIConfiguration({ github, identity: b, issueNumber, caseName,
  prNumber, deadline, now = Date.now, timers = { setTimeout, clearTimeout } } = {}) {
  if (b?.repo !== 'Gentleman-Programming/engram' || b.event !== 'pull_request'
    || b.job !== 'unit-tests' || typeof b.action !== 'string' || !/^[\w-]+$/.test(b.action)
    || ![b.head, b.checkout, b.workflow, b.prHead].every(sha)
    || !positive(b.runId) || !positive(b.attempt) || !positive(prNumber)
    || !positive(issueNumber) || issueNumber > 2147483647
    || typeof caseName !== 'string' || !/^[a-z0-9][a-z0-9._-]{0,99}$/.test(caseName)) throw fail('configuration');
  let last = Number.MIN_SAFE_INTEGER;
  const clock = () => {
    let n;
    try { n = now(); } catch { throw fail('expired'); }
    if (!Number.isSafeInteger(n) || n < last) throw fail('expired');
    return (last = n);
  };
  const started = clock(), until = Math.min(deadline ?? started + 15000, started + 15000);
  const check = () => { const n = clock(); if (!Number.isSafeInteger(until) || n >= until) throw fail('expired'); return n; };
  const read = async (fn) => {
    const remaining = until - check(), abort = new AbortController();
    let timer, value;
    const timeout = new Promise((_, reject) => {
      timer = timers.setTimeout(() => { abort.abort(); reject(fail('transport')); }, remaining);
    });
    try { value = await Promise.race([fn({ timeout: remaining, signal: abort.signal }), timeout]); }
    catch { throw fail('transport'); }
    finally { timers.clearTimeout(timer); }
    check();
    return value;
  };
  const source = { owner: 'Gentleman-Programming', repo: 'engram' };
  const response = await read((request) => github.rest.repos.getContent({ ...source,
    path: '.github/ci-proof-controllers.json', ref: 'main', request }));
  const data = response?.data;
  if (data?.type !== 'file' || data.encoding !== 'base64' || !sha(data.sha)
    || typeof data.content !== 'string' || data.content.length > 16384) throw fail('source');
  const bytes = Buffer.from(data.content, 'base64');
  let policy;
  try { if (bytes.length > 8192) throw fail('source'); policy = JSON.parse(bytes.toString('utf8')); }
  catch { throw fail('source'); }
  if (policy?.version !== 1 || !Array.isArray(policy.controllerIds)
    || !policy.controllerIds.length || !policy.controllerIds.every(positive)) throw fail('source');
  const snapshot = await read((request) => github.graphql(query, { ...source, issue: issueNumber, request }));
  const nodes = snapshot?.repository?.issue?.comments?.nodes;
  if (!Array.isArray(nodes) || nodes.length > 20) throw fail('configuration');
  const candidates = [];
  for (const node of nodes) {
    const actor = node?.author;
    if (node?.lastEditedAt !== null || actor?.__typename !== 'User'
      || !positive(actor.databaseId) || !policy.controllerIds.includes(actor.databaseId)
      || typeof node.body !== 'string' || Buffer.byteLength(node.body) > 8192) continue;
    let envelope;
    try { envelope = JSON.parse(node.body); } catch { continue; }
    if (envelope?.schema !== 'ci-proof-config/v1' || envelope.prNumber !== prNumber
      || envelope.manifest?.case !== caseName || envelope.manifest?.target?.prHead !== b.prHead) continue;
    candidates.push({ node, cfg: envelope.manifest });
  }
  // Uniqueness only within this bounded observation; never choose newest-wins.
  if (candidates.length !== 1) throw fail('configuration');
  const { node, cfg } = candidates[0], optional = ['runId', 'attempt', 'workflow', 'head'];
  if (cfg.enabled !== true || cfg.issueNumber !== issueNumber
    || Object.keys(b).some((key) => (!optional.includes(key) || key in cfg.target) && cfg.target[key] !== b[key])
    || !Array.isArray(cfg.actorIds) || !cfg.actorIds.length || !cfg.actorIds.every(positive)
    || !Number.isSafeInteger(cfg.expiresAt) || cfg.expiresAt <= check() || cfg.expiresAt - clock() > 300000
    || !positive(cfg.maxReads) || cfg.maxReads > 10
    || !positive(cfg.intervalMs) || cfg.intervalMs > 1000) throw fail('configuration');
  const result = { raw: JSON.stringify(cfg), receipt: { event: 'configuration-resolved',
    controllerId: node.author.databaseId, policyBlob: data.sha,
    policySha256: digest(bytes), configSha256: digest(node.body) } };
  if (cfg.expiresAt <= check()) throw fail('expired');
  return result;
}
