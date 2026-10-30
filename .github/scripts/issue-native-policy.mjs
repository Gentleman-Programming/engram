import { evaluateIssueValidation } from './issue-validation.mjs';

const actions = new Set(['opened', 'edited', 'labeled', 'unlabeled', 'synchronize', 'reopened']);

/** Read-only native verdict, bound to the original run rather than its base SHA. */
export async function evaluateNativeIssuePolicy(github, context, policy) {
  if (!['reference', 'approved'].includes(policy)) throw new Error('Unsupported native policy');
  if (context?.eventName !== 'pull_request_target' || !actions.has(context.payload?.action)) {
    throw new Error('Unsupported native event or action');
  }
  const originalHead = context.payload?.pull_request?.head?.sha;
  if (typeof originalHead !== 'string' || !/^[0-9a-f]{40}$/i.test(originalHead)) {
    throw new Error('Invalid original PR head SHA');
  }
  const evaluations = await evaluateIssueValidation(github, context);
  if (!Array.isArray(evaluations) || evaluations.length !== 1) {
    throw new Error('Missing or ambiguous native policy target');
  }
  const evaluation = evaluations[0];
  if (evaluation.headSha !== originalHead) throw new Error('Native PR head drift; fresh run required');
  const verdict = evaluation[policy];
  if (!verdict || !['success', 'failure'].includes(verdict.conclusion) || typeof verdict.summary !== 'string') {
    throw new Error('Malformed native policy verdict');
  }
  return { headSha: originalHead, ...verdict };
}
