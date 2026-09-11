import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

test('keeps the required PR label check on trusted workflow bytes', () => {
  const workflow = fs.readFileSync('.github/workflows/pr-label-check.yml', 'utf8');
  const legacyWorkflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');

  assert.match(
    workflow,
    /^  pull_request_target:\r?\n    types: \[opened, edited, labeled, unlabeled, synchronize, reopened\]/m,
  );
  assert.match(workflow, /name: Check PR Has type:\* Label/);
  assert.doesNotMatch(workflow, /pull_request\.head|head\.sha/);

  for (const candidate of [workflow, legacyWorkflow]) {
    assert.match(
      candidate,
      /^    concurrency:\r?\n      group: \$\{\{ github\.workflow \}\}-type-label-\$\{\{ github\.event\.pull_request\.number \}\}\r?\n      cancel-in-progress: true$/m,
    );
  }

  if (workflow.includes('actions/checkout')) {
    assert.match(workflow, /ref: \$\{\{ github\.event\.pull_request\.base\.sha \}\}/);
  }

  assert.match(workflow, /typeLabels\.length|label-policy\.mjs/);
});
