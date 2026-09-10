import assert from 'node:assert/strict';
import test from 'node:test';

import {
  findTransientArtifacts,
  isTransientArtifactPath,
  listPullRequestFiles,
} from './transient-artifacts.mjs';

test('identifies evidence-backed transient artifact paths', () => {
  const rejectedPaths = [
    '.atl/skill-registry.md',
    '.DS_Store',
    'foo.db',
    'foo.swp',
    'foo~',
    'foo.exe',
    'engram-export.json',
    'plan.md',
    'agent-report.md',
    'agent-handoff.md',
    'handoff.md',
    'openspec/changes/reject-artifacts/proposal.md',
    'sdd/changes/reject-artifacts/tasks.md',
  ];

  for (const filePath of rejectedPaths) {
    assert.equal(isTransientArtifactPath(filePath), true, filePath);
  }
});

test('allows reviewed and documented files', () => {
  const allowedPaths = [
    'docs/new-guide.md',
    'CONTRIBUTING.md',
    '.deadcode-baseline.txt',
    '.perf-baseline.txt',
    'internal/cloud/dashboard/page_templ.go',
    'docs/plan.md',
    'specs/transient-artifact-policy.md',
  ];

  for (const filePath of allowedPaths) {
    assert.equal(isTransientArtifactPath(filePath), false, filePath);
  }
});

test('restricts generated agent-link rules to repository-root paths', () => {
  assert.equal(isTransientArtifactPath('.claude/skills/skill.md'), true);
  assert.equal(isTransientArtifactPath('fixtures/.claude/skills/skill.md'), false);
});

test('allows deleted artifacts and rejects renamed destinations', () => {
  const artifacts = findTransientArtifacts([
    { filename: 'cmd/engram/main.go', status: 'added' },
    { filename: 'docs/new-guide.md', status: 'modified' },
    { filename: 'old.db', status: 'removed' },
    { filename: 'plan.md', status: 'added' },
    { filename: 'openspec/changes/reject-artifacts/proposal.md', status: 'copied' },
    { filename: 'new.db', status: 'renamed' },
  ]);

  assert.deepEqual(artifacts.map(file => file.filename), [
    'plan.md',
    'openspec/changes/reject-artifacts/proposal.md',
    'new.db',
  ]);
});

test('enumerates all pull request files through GitHub pagination', async () => {
  const listFiles = () => {};
  const github = {
    rest: { pulls: { listFiles } },
    paginate: async (endpoint, parameters) => {
      assert.equal(endpoint, listFiles);
      assert.deepEqual(parameters, {
        owner: 'Gentleman-Programming',
        repo: 'engram',
        pull_number: 1093,
        per_page: 100,
      });
      return [{ filename: 'docs/new-guide.md', status: 'added' }];
    },
  };

  const files = await listPullRequestFiles(github, {
    owner: 'Gentleman-Programming',
    repo: 'engram',
    pullNumber: 1093,
  });

  assert.equal(files.length, 1);
});

test('propagates pull request file enumeration failures', async () => {
  const failure = new Error('GitHub API unavailable');
  const github = {
    rest: { pulls: { listFiles: () => {} } },
    paginate: async () => {
      throw failure;
    },
  };

  await assert.rejects(
    listPullRequestFiles(github, { owner: 'Gentleman-Programming', repo: 'engram', pullNumber: 1093 }),
    failure,
  );
});
