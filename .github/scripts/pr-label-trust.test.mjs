import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';

test('keeps the required PR label check on trusted workflow bytes', () => {
  const workflow = fs.readFileSync('.github/workflows/pr-label-check.yml', 'utf8');

  assert.match(workflow, /^  pull_request_target:/m);
  assert.match(workflow, /types: \[opened, edited, labeled, unlabeled, synchronize, reopened\]/);
  assert.match(workflow, /name: Check PR Has type:\* Label/);
  assert.doesNotMatch(workflow, /pull_request\.head|head\.sha/);

  if (workflow.includes('actions/checkout')) {
    assert.match(workflow, /ref: \$\{\{ github\.event\.pull_request\.base\.sha \}\}/);
  }

  assert.match(workflow, /typeLabels\.length|label-policy\.mjs/);
});
