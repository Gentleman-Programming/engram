// Deliberately independent of required-check publication and refresh state.
export function references(body) {
  if (body == null) return [];
  if (typeof body !== 'string' || body.length > 65536) throw Error('Invalid PR body');
  const ids = new Set();
  const pattern = /(?:^|[\s(])(?:closes|fixes|resolves)[ \t]+#([1-9][0-9]*)(?=$|[\s,;!?)\]]|\.(?:\s|$))/gi;
  for (const match of body.matchAll(pattern)) {
    const id = Number(match[1]);
    if (Number.isSafeInteger(id)) ids.add(id);
  }
  return [...ids];
}

const loginValid = login => typeof login === 'string' && /^[a-z\d](?:[a-z\d-]{0,38})$/i.test(login);
function eligibility(issue) {
  if (!issue || !['open', 'closed'].includes(issue.state) ||
      !Array.isArray(issue.labels) || !issue.labels.every(label => typeof label?.name === 'string') ||
      !Array.isArray(issue.assignees) || !issue.assignees.every(user => loginValid(user?.login))) {
    throw Error('Malformed issue metadata');
  }
  if (issue.pull_request) return 'Skipped: reference is a pull request';
  if (issue.state !== 'open') return 'Skipped: issue closed';
  if (!issue.labels.some(label => label.name === 'status:approved')) return 'Skipped: issue not approved';
  if (issue.assignees.length) return 'Skipped: existing ownership preserved';
  return null;
}

export async function autoAssign(github, context, core) {
  const lines = ['## Linked issue assignment'];
  let failed = false;
  try {
    const pull_number = context.payload.pull_request.number;
    if (!Number.isSafeInteger(pull_number) || pull_number <= 0) throw Error('Invalid PR number');
    const { owner, repo } = context.repo;
    const { data: pr } = await github.rest.pulls.get({ owner, repo, pull_number });
    if (!pr || typeof pr.draft !== 'boolean' || !['open', 'closed'].includes(pr.state) ||
        !pr.user || !['User', 'Bot'].includes(pr.user.type)) throw Error('Malformed PR metadata');
    if (pr.state !== 'open' || pr.draft || pr.user.type === 'Bot' || /\[bot\]$/i.test(pr.user.login)) {
      lines.push('Skipped: PR is closed, draft or bot-authored');
    } else {
      if (!loginValid(pr.user.login)) throw Error('Malformed PR author');
      const ids = references(pr.body);
      if (!ids.length) lines.push('Skipped: no supported local closing references');
      for (const issue_number of ids) {
        const args = { owner, repo, issue_number };
        try {
          let reason = eligibility((await github.rest.issues.get(args)).data);
          if (!reason) reason = eligibility((await github.rest.issues.get(args)).data);
          if (reason) { lines.push(`#${issue_number}: ${reason}`); continue; }
          await github.rest.issues.addAssignees({ ...args, assignees: [pr.user.login] });
          const verified = (await github.rest.issues.get(args)).data;
          // Validate metadata before accepting an API success (GitHub can silently ignore users).
          eligibility(verified);
          if (!verified.assignees.some(user => user.login.toLowerCase() === pr.user.login.toLowerCase())) {
            throw Error('Assignment not verified');
          }
          lines.push(`#${issue_number}: Assigned and verified`);
        } catch {
          failed = true;
          lines.push(`#${issue_number}: Failed: API, metadata or assignment verification error; inspect current ownership before retrying`);
        }
      }
    }
  } catch {
    failed = true;
    lines.push('Failed: unable to evaluate current PR metadata');
  }
  try { await core.summary.addRaw(`${lines.join('\n\n')}\n`).write(); }
  catch { failed = true; }
  if (failed) core.setFailed('Issue assignment automation failed; consult the run summary and current issue ownership.');
}
