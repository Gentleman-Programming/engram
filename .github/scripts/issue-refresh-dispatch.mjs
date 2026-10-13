import { closingIssueNumbers } from './issue-validation.mjs';
import { rerunNativeIssuePolicy } from './issue-refresh-router.mjs';

const positive = (value) => Number.isSafeInteger(value) && value > 0;
function requireEvidence(condition, message) {
  if (!condition) throw new Error(`Issue refresh discovery blocked: ${message}`);
}

/** Inactive issue-event discovery; the existing helper exclusively owns rerun admission. */
export async function dispatchIssueApprovalRefresh(github, context, trustedSha) {
  const { eventName, payload = {}, repo } = context;
  if (eventName !== 'issues' || !['labeled', 'unlabeled'].includes(payload.action)
      || payload.label?.name !== 'status:approved' || payload.issue?.pull_request
      || !positive(payload.issue?.number)) return [];
  requireEvidence(typeof repo?.owner === 'string' && repo.owner.length > 0
    && typeof repo?.repo === 'string' && repo.repo.length > 0
    && typeof trustedSha === 'string' && /^[a-f0-9]{40}$/.test(trustedSha), 'trusted repository/SHA unavailable');
  const request = async (route, params) => {
    try {
      return (await github.request(route, { ...repo, ...params })).data;
    } catch (error) {
      throw new Error(`Issue refresh discovery blocked: ${error.message}`, { cause: error });
    }
  };
  const numbers = new Set();
  for (let page = 1; ; page += 1) {
    const listing = await request('GET /repos/{owner}/{repo}/pulls', { state: 'open', per_page: 100, page });
    requireEvidence(Array.isArray(listing) && listing.length <= 100, 'open PR listing malformed');
    for (const pull of listing) {
      requireEvidence(positive(pull?.number) && !numbers.has(pull.number), 'open PR number malformed/repeated');
      numbers.add(pull.number);
    }
    if (listing.length < 100) break;
  }
  const affected = [];
  // Finish every discovery read before allowing the first helper to issue a POST.
  for (const number of numbers) {
    const pull = await request('GET /repos/{owner}/{repo}/pulls/{pull_number}', { pull_number: number });
    requireEvidence(pull?.number === number && ['open', 'closed'].includes(pull.state)
      && (pull.body === null || typeof pull.body === 'string'), 'current PR metadata malformed');
    if (pull.state === 'open' && closingIssueNumbers(pull.body).includes(payload.issue.number)) affected.push(pull);
  }
  const results = [], failures = [];
  for (const pull of affected) {
    try {
      results.push(await rerunNativeIssuePolicy(github, repo, pull, trustedSha));
    } catch (error) {
      failures.push({ pullNumber: pull.number, error });
    }
  }
  if (failures.length) {
    const error = new AggregateError(failures.map((failure) => failure.error),
      `Issue refresh blocked for PRs: ${failures.map((failure) => failure.pullNumber).join(', ')}`);
    error.results = results;
    error.failures = failures;
    throw error;
  }
  return results;
}
