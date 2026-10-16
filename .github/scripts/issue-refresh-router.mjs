import { createHash } from 'node:crypto';

const workflowPath = '.github/workflows/issue-policy.yml';
const workflowId = 'issue-policy.yml';
const jobNames = ['Check Issue Reference', 'Check Issue Has status:approved'];
const protectedPaths = [workflowPath, '.github/scripts/issue-native-policy.mjs', '.github/scripts/issue-validation.mjs'];
const shaValid = (value) => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
const positive = (value) => Number.isSafeInteger(value) && value > 0;
const text = (value) => typeof value === 'string' && value.length > 0;
function requireEvidence(condition, message) {
  if (!condition) throw new Error(message);
}
function binding(pull, upstream) {
  requireEvidence(positive(pull?.number) && pull.state === 'open'
    && (pull.body === null || typeof pull.body === 'string') && shaValid(pull.head?.sha)
    && text(pull.head?.ref) && text(pull.head?.repo?.full_name) && positive(pull.head.repo.id)
    && text(pull.base?.ref) && pull.base?.repo?.full_name === upstream && positive(pull.base.repo.id),
  'open PR binding is missing or malformed');
  return JSON.stringify([pull.number, pull.state, pull.body, pull.head.sha, pull.head.ref,
    pull.head.repo.full_name, pull.head.repo.id, pull.base.ref, pull.base.repo.full_name, pull.base.repo.id]);
}
function runMatches(run, pull, upstream, workflow) {
  return positive(run?.id) && run.workflow_id === workflow && run.path === workflowPath
    && run.event === 'pull_request_target' && run.head_sha === pull.head.sha
    && run.head_branch === pull.head.ref && run.repository?.full_name === upstream
    && run.head_repository?.full_name === pull.head.repo.full_name;
}
function runTargetMatches(run, pull) {
  // An absent association is not proof; both trusted job markers are always required below.
  if (Array.isArray(run.pull_requests) && run.pull_requests.length === 0) return true;
  const target = Array.isArray(run.pull_requests) && run.pull_requests.length === 1 && run.pull_requests[0];
  return target?.number === pull.number && target.base?.ref === pull.base.ref
    && target.base?.repo?.id === pull.base.repo.id && target.head?.sha === pull.head.sha
    && target.head?.ref === pull.head.ref && target.head?.repo?.id === pull.head.repo.id;
}
function originalEvent(step, pull, run) {
  try {
    const prefix = 'Original issue event v1 ';
    if (!step.name.startsWith(prefix) || step.status !== 'completed' || step.conclusion !== 'success') return null;
    const record = JSON.parse(step.name.slice(prefix.length));
    const expected = ['pull_request_target', record?.[1], pull.base.repo.id, run.id, run.run_attempt,
      pull.number, pull.head.repo.id, pull.head.sha, pull.head.ref, pull.base.repo.id, pull.base.ref];
    return Array.isArray(record) && record.length === expected.length
      && ['opened', 'edited', 'labeled', 'unlabeled', 'synchronize', 'reopened'].includes(record[1])
      && record.every((value, index) => value === expected[index]) ? JSON.stringify(record) : null;
  } catch {
    return null;
  }
}
function policyBlob(file) {
  requireEvidence(file?.type === 'file' && file.encoding === 'base64' && shaValid(file.sha)
    && typeof file.content === 'string' && Number.isSafeInteger(file.size) && file.size > 0,
  'protected policy file binding unavailable');
  const encoded = file.content.replace(/\s/g, '');
  const bytes = Buffer.from(encoded, 'base64');
  requireEvidence(bytes.toString('base64') === encoded && bytes.length === file.size
    && createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex') === file.sha,
  'protected policy blob binding invalid');
  return { sha: file.sha, bytes };
}

/** Inactive foundation: caller proves the affected closing reference; no discovery here. */
export async function rerunNativeIssuePolicy(github, repo, pullRequest, trustedSha) {
  try {
    requireEvidence(text(repo?.owner) && text(repo?.repo) && shaValid(trustedSha), 'trusted repository/SHA unavailable');
    const upstream = `${repo.owner}/${repo.repo}`;
    const expected = binding(pullRequest, upstream);
    const request = async (route, params = {}) => (await github.request(route, { owner: repo.owner, repo: repo.repo, ...params })).data;
    const readTarget = async () => {
      const current = await request('GET /repos/{owner}/{repo}/pulls/{pull_number}', { pull_number: pullRequest.number });
      requireEvidence(binding(current, upstream) === expected, 'PR metadata changed; fresh reconciliation required');
      return current;
    };
    const pull = await readTarget();
    const workflow = await request('GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}', { workflow_id: workflowId });
    requireEvidence(positive(workflow?.id) && workflow.path === workflowPath && workflow.state === 'active', 'native workflow unavailable');
    const candidates = [];
    let expectedCount;
    for (let page = 1; ; page += 1) {
      const listing = await request('GET /repos/{owner}/{repo}/actions/workflows/{workflow_id}/runs', {
        workflow_id: workflowId, event: 'pull_request_target', branch: pull.head.ref,
        head_sha: pull.head.sha, per_page: 100, page,
      });
      requireEvidence(Array.isArray(listing?.workflow_runs) && Number.isSafeInteger(listing.total_count)
        && listing.total_count >= 0, 'native run listing unavailable');
      expectedCount ??= listing.total_count;
      requireEvidence(listing.total_count === expectedCount && candidates.length + listing.workflow_runs.length <= expectedCount,
        'native run pagination changed or exceeded count');
      for (const run of listing.workflow_runs) {
        requireEvidence(positive(run?.id) && !candidates.some((candidate) => candidate.id === run.id), 'native run listing malformed/repeated');
        candidates.push(run);
      }
      if (listing.workflow_runs.length < 100) break;
    }
    requireEvidence(candidates.length === expectedCount, 'native run listing incomplete; no older-run fallback');
    // Never fall back to an older source/job shape when the newest matching run fails admission.
    const selected = candidates.filter((run) => runMatches(run, pull, upstream, workflow.id)).sort((a, b) => b.id - a.id)[0];
    requireEvidence(selected, 'no eligible native metadata run');
    requireEvidence(runTargetMatches(selected, pull), 'native run PR/event binding unavailable');
    const run = await request('GET /repos/{owner}/{repo}/actions/runs/{run_id}', { run_id: selected.id });
    requireEvidence(runMatches(run, pull, upstream, workflow.id) && run.id === selected.id
      && positive(run.run_attempt) && run.run_attempt < 50 && run.status === 'completed', 'native run binding/attempt unavailable');
    requireEvidence(runTargetMatches(run, pull), 'native run PR/event binding unavailable');
    const age = Date.now() - Date.parse(run.created_at);
    requireEvidence(Number.isFinite(age) && age >= 0 && age < 30 * 86400000, 'native run outside rerun horizon');
    const listing = await request('GET /repos/{owner}/{repo}/actions/runs/{run_id}/attempts/{attempt_number}/jobs', {
      run_id: run.id, attempt_number: run.run_attempt, per_page: 100, page: 1,
    });
    requireEvidence(listing?.total_count === 2 && Array.isArray(listing.jobs) && listing.jobs.length === 2
      && new Set(listing.jobs.map((job) => job.name)).size === 2, 'native metadata-only two-job shape unavailable');
    const proofs = listing.jobs.map((job) => {
      requireEvidence(jobNames.includes(job.name) && job.run_id === run.id
        && job.head_sha === pull.head.sha && job.status === 'completed' && Array.isArray(job.steps), 'native job binding invalid');
      const markers = job.steps.filter((step) => /^Load trusted issue policy/.test(step.name));
      const match = markers.length === 1 && /^Load trusted issue policy \(([a-f0-9]{40})\)$/.exec(markers[0].name);
      requireEvidence(match && markers[0].status === 'completed' && markers[0].conclusion === 'success', 'trusted checkout source binding unavailable');
      const identities = job.steps.filter((step) => typeof step?.name === 'string' && step.name.startsWith('Original issue event'));
      const event = identities.length === 1 && originalEvent(identities[0], pull, run);
      requireEvidence(event, 'native original PR/event binding unavailable');
      return { source: match[1], event };
    });
    requireEvidence(proofs[0].event === proofs[1].event, 'native original PR/event binding unavailable');
    requireEvidence(proofs[0].source === proofs[1].source, 'native jobs disagree on source SHA');
    const sourceSha = proofs[0].source;
    const comparison = await request('GET /repos/{owner}/{repo}/compare/{basehead}', { basehead: `${sourceSha}...${trustedSha}` });
    requireEvidence(['ahead', 'identical'].includes(comparison?.status)
      && comparison.merge_base_commit?.sha === sourceSha, 'policy source is not trusted ancestor');
    for (const path of protectedPaths) {
      const source = policyBlob(await request('GET /repos/{owner}/{repo}/contents/{path}', { path, ref: sourceSha }));
      const current = policyBlob(await request('GET /repos/{owner}/{repo}/contents/{path}', { path, ref: trustedSha }));
      requireEvidence(source.sha === current.sha && source.bytes.equals(current.bytes), `protected policy changed: ${path}`);
    }
    await readTarget(); // Narrows, but does not eliminate, the metadata-to-request race.
    const response = await github.request('POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun', {
      owner: repo.owner, repo: repo.repo, run_id: run.id,
    });
    requireEvidence(response?.status === 201, 'rerun request not acknowledged');
    return { status: 'requested', runId: run.id, headSha: pull.head.sha, sourceSha, trustedSha, attempt: run.run_attempt };
  } catch (error) {
    throw new Error(`Issue refresh blocked: ${error.message}`, { cause: error });
  }
}
