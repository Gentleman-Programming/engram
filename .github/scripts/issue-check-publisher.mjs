const policies = [
  ['reference', 'Check Issue Reference'],
  ['approved', 'Check Issue Has status:approved'],
];

function exactFields(value, fields) {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    && Object.keys(value).length === fields.length
    && fields.every((field) => Object.hasOwn(value, field));
}

function nonempty(value) {
  return typeof value === 'string' && value.trim().length > 0;
}

// Validate and snapshot the whole batch before the first asynchronous write.
// Never update a legacy job's run: each reconciliation creates its own evidence.
export async function publishIssueChecks(github, repo, evaluations) {
  if (!exactFields(repo, ['owner', 'repo']) || !nonempty(repo.owner) || !nonempty(repo.repo)
      || !Array.isArray(evaluations)) {
    throw new Error('Invalid issue-check repository or evaluation batch');
  }
  const requests = [];
  for (const evaluation of evaluations) {
    if (!exactFields(evaluation, ['headSha', 'reference', 'approved'])
        || typeof evaluation.headSha !== 'string' || !/^[a-f0-9]{40}$/i.test(evaluation.headSha)) {
      throw new Error('Invalid issue-check evaluation: explicit full head SHA required');
    }
    for (const [field, name] of policies) {
      const result = evaluation[field];
      if (!exactFields(result, ['conclusion', 'summary'])
          || !['success', 'failure'].includes(result.conclusion) || !nonempty(result.summary)) {
        throw new Error('Invalid issue-check evaluation: success/failure and summary required');
      }
      requests.push({
        owner: repo.owner,
        repo: repo.repo,
        name,
        head_sha: evaluation.headSha,
        status: 'completed',
        conclusion: result.conclusion,
        output: { title: name, summary: result.summary },
      });
    }
  }
  if (requests.length === 0) return;
  if (typeof github?.rest?.checks?.create !== 'function') {
    throw new Error('Issue-check publisher blocker: Checks create capability unavailable');
  }
  for (const request of requests) {
    let response;
    try {
      response = await github.rest.checks.create(request);
    } catch (error) {
      throw new Error(`Issue-check publisher blocker: Checks create failed (${error.message}); do not broaden credentials`, { cause: error });
    }
    const run = response?.data;
    if (run?.app?.id !== 15368 || run.head_sha !== request.head_sha
        || run.name !== request.name || run.status !== 'completed' || run.conclusion !== request.conclusion) {
      throw new Error('Issue-check publisher blocker: returned check lacks GitHub Actions App15368/head/name/result binding');
    }
  }
}
