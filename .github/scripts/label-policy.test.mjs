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
    'resolution:duplicate',
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
  assert.deepEqual(errorsFor(['type:feature', 'good first issue', 'help wanted']), []);
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

test('runs PR label policy from the trusted base with shell-safe label JSON', () => {
  const prWorkflow = fs.readFileSync('.github/workflows/pr-check.yml', 'utf8');
  const labelWorkflow = fs.readFileSync('.github/workflows/pr-label-check.yml', 'utf8');
  assert.match(prWorkflow, /^  pull_request:/m);
  assert.doesNotMatch(prWorkflow, /^  pull_request_target:/m);
  assert.match(labelWorkflow, /^  pull_request_target:/m);
  assert.match(labelWorkflow, /ref: \$\{\{ github\.event\.pull_request\.base\.sha \}\}/);
  assert.match(labelWorkflow, /PR_LABELS: \$\{\{ toJson\(github\.event\.pull_request\.labels\.\*\.name\) \}\}/);
  assert.match(labelWorkflow, /--labels-json "\$PR_LABELS"/);
  assert.doesNotMatch(labelWorkflow, /--labels-json '\$\{\{/);
});

test('migrates deprecated labels idempotently and preserves ambiguous combinations', () => {
  const aliases = [
    ['bug', 'type:bug'],
    ['enhancement', 'type:feature'],
    ['question', 'type:question'],
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

test('documents singleton-safe duplicate status transitions', () => {
  const skill = fs.readFileSync('skills/issue-creation/SKILL.md', 'utf8');
  const duplicateCommands = skill.slice(skill.indexOf('# Maintainer: clear every active status while evaluating'));

  assert.match(duplicateCommands, /select\(startswith\("status:"\)\)/);
  assert.match(duplicateCommands, /--remove-label "\$status"/);
  assert.match(duplicateCommands, /--add-label "status:possible-duplicate"/);
});
