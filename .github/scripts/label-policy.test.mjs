import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import test from 'node:test';
import { loadLabelPolicy, migrateLabels, validateLabels } from './label-policy.mjs';

const policy = loadLabelPolicy();

function errorsFor(labels, target = 'issue') {
  return validateLabels(policy, labels, target).errors;
}

test('validates canonical label combinations without a global maximum', () => {
  const labels = [
    'type:bug',
    'status:possible-duplicate',
    'priority:critical',
    'effort:small',
    'effort:medium',
    'effort:large',
    'good first issue',
    'help wanted',
    ...Array(100).fill('effort:small'),
  ];

  assert.deepEqual(errorsFor(labels), []);
});

test('rejects missing type, singleton conflicts, unknown labels, and deprecated aliases', () => {
  const cases = [
    { name: 'missing required type', labels: ['status:needs-review'], expected: 'requires exactly one type:* label' },
    { name: 'missing required issue status', labels: ['type:bug'], expected: 'requires exactly one status:* label' },
    { name: 'type conflict', labels: ['type:bug', 'type:docs'], expected: 'type:* permits at most one label' },
    { name: 'status conflict', labels: ['type:bug', 'status:approved', 'status:blocked'], expected: 'status:* permits at most one label' },
    { name: 'priority conflict', labels: ['type:bug', 'priority:high', 'priority:low'], expected: 'priority:* permits at most one label' },
    { name: 'resolution conflict', labels: ['type:bug', 'resolution:duplicate', 'resolution:duplicate'], expected: 'resolution:* permits at most one label' },
    { name: 'unknown label', labels: ['type:bug', 'needs-triage'], expected: 'undeclared label: needs-triage' },
    { name: 'deprecated alias', labels: ['bug'], expected: 'deprecated label: bug; use type:bug' },
  ];

  for (const { name, labels, expected } of cases) {
    assert.ok(errorsFor(labels).some((error) => error.includes(expected)), name);
  }
});

test('allows only declared protected unnamespaced exceptions', () => {
  assert.deepEqual(errorsFor(['type:feature', 'status:needs-review', 'good first issue', 'help wanted']), []);
});

test('keeps issue template initial labels canonical', () => {
  const templates = [
    '.github/ISSUE_TEMPLATE/bug_report.yml',
    '.github/ISSUE_TEMPLATE/feature_request.yml',
    '.github/ISSUE_TEMPLATE/docs_improvement.yml',
    '.github/ISSUE_TEMPLATE/tracked_question.yml',
  ];
  for (const template of templates) {
    const labels = fs.readFileSync(template, 'utf8').match(/^labels: \[(.*)]$/m)[1]
      .split(',').map((label) => label.trim().replace(/^['"]|['"]$/g, ''));
    assert.deepEqual(errorsFor(labels), [], template);
  }
});

test('rejects labels outside their declared applicability', () => {
  assert.ok(
    errorsFor(['type:bug', 'priority:high'], 'pull-request').includes(
      'label priority:high does not apply to pull-request',
    ),
  );
  assert.ok(
    errorsFor(['type:bug', 'good first issue'], 'pull-request').includes(
      'label good first issue does not apply to pull-request',
    ),
  );
  assert.deepEqual(errorsFor(['type:chore', 'size:exception'], 'pull-request'), []);
  assert.deepEqual(errorsFor(['type:chore'], 'pull-request'), []);
  assert.ok(
    errorsFor(['type:chore', 'size:exception']).includes(
      'label size:exception does not apply to issue',
    ),
  );
  assert.equal(
    policy.entries.find((entry) => entry.name === 'size:exception')?.policy.protected_exception,
    true,
  );
});

test('declares triage policy as maintainer-owned, optional, and issue-only', () => {
  const triage = policy.namespaces.get('triage');

  assert.equal(triage?.owner, 'maintainers');
  assert.equal(triage?.cardinality, 'optional-singleton');
  assert.deepEqual(triage?.appliesTo, ['issue']);
  assert.deepEqual(errorsFor(['type:bug', 'status:needs-review', 'triage:possible-duplicate']), []);
  assert.ok(
    errorsFor(['type:chore', 'triage:possible-duplicate'], 'pull-request').includes(
      'label triage:possible-duplicate does not apply to pull-request',
    ),
  );
});

test('declares ownership and migration precedence as machine-readable policy', () => {
  for (const entry of policy.entries) {
    if (entry.name.includes(':')) {
      const namespace = entry.name.split(':', 1)[0];
      assert.equal(policy.namespaces.get(namespace)?.owner, 'maintainers', entry.name);
    } else {
      assert.equal(entry.policy.owner, 'maintainers', entry.name);
    }
  }

  assert.deepEqual(policy.namespaces.get('status')?.requiredOn, ['issue']);
  assert.deepEqual([...policy.migrations], [
    ['bug', { canonical: 'type:bug', precedence: 'canonical', conflict: 'manual-review' }],
    ['enhancement', { canonical: 'type:feature', precedence: 'canonical', conflict: 'manual-review' }],
    ['question', { canonical: 'type:question', precedence: 'canonical', conflict: 'manual-review' }],
    ['documentation', { canonical: 'type:docs', precedence: 'canonical', conflict: 'manual-review' }],
    ['up for grabs', { canonical: 'help wanted', precedence: 'canonical', conflict: 'manual-review' }],
  ]);
});

test('runs PR label policy from the trusted base with current API labels', () => {
  const prWorkflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
  const labelWorkflow = fs.readFileSync('.github/workflows/pr-label-check.yml', 'utf8');
  assert.match(prWorkflow, /^  pull_request:/m);
  assert.doesNotMatch(prWorkflow, /^  pull_request_target:/m);
  assert.match(
    labelWorkflow,
    /^  pull_request_target:\r?\n    types: \[opened, edited, labeled, unlabeled, synchronize, reopened\]/m,
  );
  assert.match(labelWorkflow, /name: Check PR Has type:\* Label/);
  assert.doesNotMatch(labelWorkflow, /name: Check PR Label Policy/);
  assert.match(
    labelWorkflow,
    /^  check-label-policy:\r?\n    name: Check PR Has type:\* Label\r?\n    runs-on: ubuntu-latest\r?\n    concurrency:\r?\n      group: \$\{\{ github\.workflow \}\}-type-label-\$\{\{ github\.event\.pull_request\.number \}\}\r?\n      cancel-in-progress: true$/m,
  );
  assert.match(labelWorkflow, /^  pull-requests: read$/m);
  assert.match(labelWorkflow, /id: current-labels/);
  assert.match(labelWorkflow, /github\.rest\.pulls\.get\(\{/);
  assert.match(labelWorkflow, /pull_number: context\.issue\.number/);
  assert.match(labelWorkflow, /ref: \$\{\{ github\.event\.pull_request\.base\.sha \}\}/);
  assert.match(labelWorkflow, /PR_LABELS: \$\{\{ steps\.current-labels\.outputs\.result \}\}/);
  assert.doesNotMatch(labelWorkflow, /github\.event\.pull_request\.labels|context\.payload\.pull_request\.labels/);
  assert.doesNotMatch(labelWorkflow, /pull_request\.head|head\.sha/);
  assert.match(labelWorkflow, /--labels-json "\$PR_LABELS"/);
  assert.doesNotMatch(labelWorkflow, /--labels-json '\$\{\{/);
});

test('documents the PR label policy as a required status check', () => {
  const contributing = fs.readFileSync('CONTRIBUTING.md', 'utf8');

  assert.match(
    contributing,
    /> \*\*Repo admin note:\*\*[^\r\n]*`Check PR Has type:\* Label`/,
  );
});

test('migrates deprecated labels idempotently and preserves ambiguous combinations', () => {
  const aliases = [
    ['bug', 'type:bug'],
    ['enhancement', 'type:feature'],
    ['question', 'type:question'],
    ['documentation', 'type:docs'],
    ['up for grabs', 'help wanted'],
  ];
  for (const [alias, canonical] of aliases) {
    const migrated = migrateLabels(policy, [alias]);
    assert.deepEqual(migrated.labels, [canonical]);
    assert.equal(migrated.changed, true);
    assert.deepEqual(migrateLabels(policy, migrated.labels), { labels: migrated.labels, changed: false, conflicts: [] });
  }

  assert.deepEqual(migrateLabels(policy, ['bug', 'type:bug']), {
    labels: ['type:bug'],
    changed: true,
    conflicts: [],
  });

  const ambiguous = migrateLabels(policy, ['bug', 'type:feature']);
  assert.deepEqual(ambiguous.labels, ['bug', 'type:feature']);
  assert.deepEqual(ambiguous.conflicts, ['type:* permits at most one label: type:bug, type:feature']);
  assert.equal(ambiguous.changed, false);
});

test('exposes migration as an idempotent dry-run CLI', () => {
  const command = spawnSync(
    process.execPath,
    ['.github/scripts/label-policy.mjs', '--migrate', '--labels-json', JSON.stringify(['bug', 'type:bug'])],
    { encoding: 'utf8' },
  );

  assert.equal(command.status, 0, command.stderr);
  assert.deepEqual(JSON.parse(command.stdout), {
    labels: ['type:bug'],
    changed: true,
    conflicts: [],
  });

  const repeated = spawnSync(
    process.execPath,
    ['.github/scripts/label-policy.mjs', '--migrate', '--labels-json', JSON.stringify(['type:bug'])],
    { encoding: 'utf8' },
  );
  assert.equal(repeated.status, 0, repeated.stderr);
  assert.deepEqual(JSON.parse(repeated.stdout), {
    labels: ['type:bug'],
    changed: false,
    conflicts: [],
  });

  const conflict = spawnSync(
    process.execPath,
    ['.github/scripts/label-policy.mjs', '--migrate', '--labels-json', JSON.stringify(['bug', 'type:feature'])],
    { encoding: 'utf8' },
  );
  assert.equal(conflict.status, 1);
  assert.deepEqual(JSON.parse(conflict.stdout), {
    labels: ['bug', 'type:feature'],
    changed: false,
    conflicts: ['type:* permits at most one label: type:bug, type:feature'],
  });
});

test('documents maintainer-owned terminal duplicate transitions', () => {
  const skill = fs.readFileSync('skills/issue-creation/SKILL.md', 'utf8');
  const contributing = fs.readFileSync('CONTRIBUTING.md', 'utf8');
  const duplicateCommands = skill.slice(skill.indexOf('# Maintainer: replace every active status while evaluating'));

  assert.deepEqual(
    validateLabels(policy, ['type:bug', 'status:wontfix', 'resolution:duplicate'], 'issue'),
    { errors: [] },
  );
  assert.match(
    contributing,
    /After confirming and closing the duplicate, replace `status:possible-duplicate` with `status:wontfix` and apply `resolution:duplicate`\./,
  );
  assert.match(
    skill,
    /Is it a confirmed duplicate\?\s+→ Close it, replace evaluation status with status:wontfix, then add resolution:duplicate/,
  );
  assert.match(duplicateCommands, /set -euo pipefail/);
  assert.match(duplicateCommands, /other_statuses="\$\(gh issue view/);
  assert.match(duplicateCommands, /--add-label "status:possible-duplicate"/);
  assert.match(duplicateCommands, /--remove-label "\$\{other_statuses\/\/\$'\\n'\/,\}"/);
  assert.match(duplicateCommands, /gh issue edit <number> "\$\{status_args\[@\]\}"/);
  assert.match(duplicateCommands, /updated_statuses="\$\(gh issue view/);
  assert.match(duplicateCommands, /"\$updated_statuses" != "status:possible-duplicate"/);
  assert.match(duplicateCommands, /stop without retrying/);
  assert.match(
    duplicateCommands,
    /--remove-label "status:possible-duplicate" --add-label "status:wontfix,resolution:duplicate"/,
  );
  assert.doesNotMatch(duplicateCommands, /while IFS=[\s\S]*gh issue edit <number> --remove-label "\$status"/);
});
