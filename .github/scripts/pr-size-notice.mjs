const budget = 400;
const unavailable = 'Size unavailable: current pull-request counts or labels could not be determined.';

function describeSize(pull) {
  const { additions, deletions, labels } = pull ?? {};
  if (![additions, deletions].every((value) => Number.isSafeInteger(value) && value >= 0)
    || !Number.isSafeInteger(additions + deletions)
    || !Array.isArray(labels)
    || !labels.every((label) => typeof label?.name === 'string')) {
    return { message: unavailable, warning: true };
  }
  const total = additions + deletions;
  if (total <= budget) {
    return { message: `Within review budget: ${total} changed lines (budget: ${budget}).`, warning: false };
  }
  if (labels.some((label) => label.name === 'size:exception')) {
    return { message: `Maintainer exception: ${total} changed lines (budget: ${budget}; size:exception present).`, warning: false };
  }
  return {
    message: `Oversized pull request: ${total} changed lines (budget: ${budget}). Consider splitting the change or requesting a maintainer exception.`,
    warning: true,
  };
}

// The only GitHub operation is a fresh metadata read. Never execute PR code or grant labels.
export async function publishSizeNotice({ github, context, core }) {
  let report;
  try {
    const { data } = await github.rest.pulls.get({
      owner: context.repo.owner,
      repo: context.repo.repo,
      pull_number: context.issue.number,
    });
    report = describeSize(data);
  } catch {
    report = { message: unavailable, warning: true };
  }
  if (report.warning) core.warning(report.message);
  else core.info(report.message);
  try {
    await core.summary.addHeading('PR size notice').addRaw(report.message).write();
  } catch {
    core.warning('PR size notice summary could not be published.');
  }
}
