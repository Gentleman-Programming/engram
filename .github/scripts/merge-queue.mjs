export async function resolveAssociatedPullRequests(github, { owner, repo, commitSha }) {
  const associatedPulls = await github.paginate(github.rest.repos.listPullRequestsAssociatedWithCommit, {
    owner,
    repo,
    commit_sha: commitSha,
  });
  const pullNumbers = [...new Set(associatedPulls.map((pull) => pull.number))];
  if (pullNumbers.length === 0) throw new Error('could not resolve associated pull requests');

  return Promise.all(pullNumbers.map(async (pull_number) => (
    await github.rest.pulls.get({ owner, repo, pull_number })
  ).data));
}

export async function aggregatePullRequestResults(pulls, validate) {
  const results = await Promise.all(pulls.map(async (pull) => ({
    pull,
    errors: await validate(pull),
  })));

  return results.flatMap(({ pull, errors }) => errors.map((error) => `PR #${pull.number}: ${error}`));
}
