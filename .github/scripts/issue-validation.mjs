const pullActions = new Set(['opened', 'edited', 'labeled', 'unlabeled', 'synchronize', 'reopened']);
const numberValid = (number) => Number.isSafeInteger(number) && number > 0;
const shaValid = (sha) => typeof sha === 'string' && /^[0-9a-f]{40}$/i.test(sha);
const references = (body) => [...(body || '').matchAll(/(?:closes|fixes|resolves)\s+#(\d+)/gi)].map((match) => Number(match[1]));
export { references as closingIssueNumbers };
const result = (errors) => ({ conclusion: errors.length ? 'failure' : 'success', summary: errors.length ? errors.join('\n') : 'Validation passed.' });
const failure = (headSha, message) => ({ headSha, reference: result([message]), approved: result([message]) });

function validatePull(pull, expectedNumber) {
  if (!numberValid(pull?.number) || (expectedNumber !== undefined && pull.number !== expectedNumber)
      || !shaValid(pull?.head?.sha) || !['open', 'closed'].includes(pull.state)
      || (pull.body !== null && typeof pull.body !== 'string')) {
    throw new Error('Current pull request metadata is malformed');
  }
  return pull;
}

async function readPull(github, repo, number) {
  return validatePull((await github.rest.pulls.get({ ...repo, pull_number: number })).data, number);
}

async function validateIssues(github, repo, pull) {
  const refs = references(pull.body);
  const referenceErrors = refs.length ? [] : [`PR #${pull.number}: no closing issue reference found`];
  const approvedErrors = [...referenceErrors];
  for (const issue_number of refs) {
    try {
      if (!numberValid(issue_number)) throw new Error('issue number is malformed');
      const issue = (await github.rest.issues.get({ ...repo, issue_number })).data;
      if (!issue || !Array.isArray(issue.labels)
          || issue.labels.some((label) => typeof label?.name !== 'string')
          || !Array.isArray(issue.assignees)
          || issue.assignees.some((owner) => typeof owner?.login !== 'string' || owner.login.length === 0)) {
        throw new Error('issue metadata is malformed');
      }
      if (!issue.labels.some((label) => label.name === 'status:approved')) {
        approvedErrors.push(`PR #${pull.number}: issue #${issue_number} lacks status:approved`);
      }
      if (issue.assignees.length === 0) {
        approvedErrors.push(`PR #${pull.number}: issue #${issue_number} has no assignee. Assign an owner before merge.`);
      }
    } catch (error) {
      approvedErrors.push(`PR #${pull.number}: issue #${issue_number} unavailable (${error.message})`);
    }
  }
  return { referenceErrors, approvedErrors };
}

async function evaluatePull(github, repo, pull) {
  const { referenceErrors, approvedErrors } = await validateIssues(github, repo, pull);
  // A final read narrows the race; it is not an atomic publication fence.
  try {
    const current = await readPull(github, repo, pull.number);
    if (current.state !== 'open' || current.body !== pull.body || current.head.sha !== pull.head.sha) {
      return failure(pull.head.sha, `PR #${pull.number}: metadata changed; fresh reconciliation required`);
    }
  } catch (error) {
    return failure(pull.head.sha, `PR #${pull.number}: metadata unavailable (${error.message})`);
  }
  return { headSha: pull.head.sha, reference: result(referenceErrors), approved: result(approvedErrors) };
}

async function discoverPulls(github, repo) {
  const numbers = new Set();
  for (let page = 1; ; page += 1) {
    const { data } = await github.rest.pulls.list({ ...repo, state: 'open', per_page: 100, page });
    if (!Array.isArray(data) || data.some((pull) => !numberValid(pull?.number))) {
      throw new Error('Open pull request listing is malformed');
    }
    for (const pull of data) {
      if (numbers.has(pull.number)) throw new Error('Open pull request pagination repeated a PR');
      numbers.add(pull.number);
    }
    if (data.length < 100) break;
  }
  const pulls = [];
  for (const number of numbers) pulls.push(await readPull(github, repo, number));
  return pulls;
}

/** Read-only policy evaluation; callers own publication and error reporting. */
export async function evaluateIssueValidation(github, context) {
  const { eventName, payload = {}, repo } = context;
  if (eventName === 'pull_request_target' || eventName === 'pull_request') {
    if (!pullActions.has(payload.action) || !numberValid(payload.pull_request?.number)) return [];
    const number = payload.pull_request.number;
    const pull = (await github.rest.pulls.get({ ...repo, pull_number: number })).data;
    try {
      validatePull(pull, number);
    } catch (error) {
      if (pull?.number === number && shaValid(pull?.head?.sha)) return [failure(pull.head.sha, error.message)];
      throw error;
    }
    if (pull.state !== 'open') return [];
    return [await evaluatePull(github, repo, pull)];
  }
  if (eventName !== 'issues' || !['labeled', 'unlabeled'].includes(payload.action)
      || payload.label?.name !== 'status:approved' || payload.issue?.pull_request
      || !numberValid(payload.issue?.number)) return [];
  const pulls = await discoverPulls(github, repo);
  const evaluations = [];
  for (const pull of pulls) {
    if (pull.state === 'open' && references(pull.body).includes(payload.issue.number)) {
      evaluations.push(await evaluatePull(github, repo, pull));
    }
  }
  return evaluations;
}
