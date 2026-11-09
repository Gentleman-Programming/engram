const mergeQueueEntriesQuery = `
  query MergeQueueEntries($owner: String!, $repo: String!, $baseRef: String!, $cursor: String) {
    repository(owner: $owner, name: $repo) {
      mergeQueue(branch: $baseRef) {
        entries(first: 100, after: $cursor) {
          nodes {
            position
            pullRequest {
              number
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
        }
      }
    }
  }
`;

function mergeQueueBaseBranch(baseRef) {
  const prefix = 'refs/heads/';
  if (typeof baseRef !== 'string' || !baseRef.startsWith(prefix) || baseRef.length === prefix.length) {
    throw new Error('invalid merge queue base ref');
  }
  return baseRef.slice(prefix.length);
}

function queueTailPullNumber(baseBranch, headRef) {
  if (typeof headRef !== 'string') throw new Error('invalid merge queue head ref');

  const queueRef = headRef.replace(/^refs\/heads\//, '');
  const prefix = `gh-readonly-queue/${baseBranch}/pr-`;
  const match = queueRef.startsWith(prefix) && queueRef.slice(prefix.length).match(/^(\d+)-[0-9a-f]{40}$/);
  if (!match) throw new Error('invalid merge queue head ref');

  return Number(match[1]);
}

async function mergeQueueEntries(github, { owner, repo, baseRef }) {
  const entries = [];
  const cursors = new Set();
  let cursor = null;

  while (true) {
    const response = await github.graphql(mergeQueueEntriesQuery, { owner, repo, baseRef, cursor });
    const page = response?.repository?.mergeQueue?.entries;
    if (!page) throw new Error('merge queue is unavailable');
    if (!Array.isArray(page.nodes) || !page.pageInfo || typeof page.pageInfo.hasNextPage !== 'boolean') {
      throw new Error('merge queue response is malformed');
    }

    entries.push(...page.nodes);
    if (!page.pageInfo.hasNextPage) return entries;

    cursor = page.pageInfo.endCursor;
    if (typeof cursor !== 'string' || cursor.length === 0) {
      throw new Error('merge queue pagination cursor is missing');
    }
    if (cursors.has(cursor)) throw new Error('merge queue pagination cursor repeated');
    cursors.add(cursor);
  }
}

function queuePrefix(entries, tailPullNumber) {
  if (entries.length === 0) throw new Error('merge queue is empty');

  let previousPosition = 0;
  for (const entry of entries) {
    if (!Number.isInteger(entry?.position) || entry.position <= previousPosition || !Number.isInteger(entry?.pullRequest?.number)) {
      throw new Error('merge queue entry is malformed');
    }
    previousPosition = entry.position;
  }

  const tailIndex = entries.findIndex((entry) => entry.pullRequest.number === tailPullNumber);
  if (tailIndex === -1) throw new Error(`merge queue tail PR #${tailPullNumber} was not found`);

  return entries.slice(0, tailIndex + 1).map((entry) => entry.pullRequest.number);
}

export async function resolveAssociatedPullRequests(github, { owner, repo, baseRef, headRef }) {
  const baseBranch = mergeQueueBaseBranch(baseRef);
  const tailPullNumber = queueTailPullNumber(baseBranch, headRef);
  const pullNumbers = queuePrefix(await mergeQueueEntries(github, { owner, repo, baseRef: baseBranch }), tailPullNumber);

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
