import assert from 'node:assert/strict';
import test from 'node:test';

import {
  findTransientArtifacts,
  isTransientArtifactPath,
  listPullRequestFiles,
} from './transient-artifacts.mjs';

test('identifies every configured transient artifact classifier and variant', () => {
  const rejectedCases = [
    { classifier: '.atl artifacts', paths: ['.atl/skill-registry.md'] },
    {
      classifier: 'generated agent-link roots',
      paths: [
        '.claude/skills/skill.md',
        '.codex/skills/skill.md',
        '.github/skills/skill.md',
        '.gemini/skills/skill.md',
      ],
    },
    { classifier: 'engram-dev artifacts', paths: ['engram-dev/state.json'] },
    {
      classifier: 'root transient development documents',
      paths: ['plan.md', 'agent-report.md', 'agent-handoff.md', 'handoff.md'],
    },
    {
      classifier: 'root transient process artifacts',
      paths: [
        'openspec/changes/reject-artifacts/proposal.md',
        'sdd/changes/reject-artifacts/tasks.md',
      ],
    },
    { classifier: 'release-notes beta artifacts', paths: ['.release-notes-beta.md'] },
    { classifier: 'local databases', paths: ['foo.db', 'foo.db-wal', 'foo.db-shm'] },
    { classifier: 'local exports', paths: ['engram-export.json'] },
    {
      classifier: 'binaries',
      paths: [
        'engram',
        'cmd/engram/main',
        'cmd/engram/gentle-creation',
        'cmd/engram/engram',
        'foo.exe',
      ],
    },
    { classifier: 'OS metadata', paths: ['.DS_Store', 'Thumbs.db'] },
    { classifier: 'editor metadata', paths: ['.idea/workspace.xml', '.vscode/settings.json'] },
    { classifier: 'editor and backup files', paths: ['foo.swp', 'foo.swo', 'foo~'] },
  ];

  for (const { classifier, paths } of rejectedCases) {
    for (const filePath of paths) {
      assert.equal(isTransientArtifactPath(filePath), true, `${classifier}: ${filePath}`);
    }
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
    'fixtures/.claude/skills/skill.md',
    'fixtures/.codex/skills/skill.md',
    'fixtures/.github/skills/skill.md',
    'fixtures/.gemini/skills/skill.md',
    'fixtures/openspec/changes/reject-artifacts/proposal.md',
    'fixtures/sdd/changes/reject-artifacts/tasks.md',
    'tools/engram',
    'fixtures/cmd/engram/main',
  ];

  for (const filePath of allowedPaths) {
    assert.equal(isTransientArtifactPath(filePath), false, filePath);
  }
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
  const get = async parameters => {
    assert.deepEqual(parameters, {
      owner: 'Gentleman-Programming',
      repo: 'engram',
      pull_number: 1093,
    });
    return { data: { changed_files: 1 } };
  };
  const listFiles = () => {};
  const github = {
    rest: { pulls: { get, listFiles } },
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

test('rejects incomplete pull request file enumeration', async () => {
  const github = {
    rest: { pulls: { get: async () => ({ data: { changed_files: 2 } }), listFiles: () => {} } },
    paginate: async () => [{ filename: 'docs/new-guide.md', status: 'added' }],
  };

  await assert.rejects(
    listPullRequestFiles(github, { owner: 'Gentleman-Programming', repo: 'engram', pullNumber: 1093 }),
    /PR #1093 reports 2 changed files, but the file API returned 1; refusing incomplete artifact validation/,
  );
});

test('propagates pull request file enumeration failures', async () => {
  const failure = new Error('GitHub API unavailable');
  const github = {
    rest: { pulls: { get: async () => ({ data: { changed_files: 1 } }), listFiles: () => {} } },
    paginate: async () => {
      throw failure;
    },
  };

  await assert.rejects(
    listPullRequestFiles(github, { owner: 'Gentleman-Programming', repo: 'engram', pullNumber: 1093 }),
    failure,
  );
});

test('propagates pull request metadata failures', async () => {
  const failure = new Error('GitHub pull request lookup unavailable');
  const github = {
    rest: { pulls: { get: async () => { throw failure; }, listFiles: () => {} } },
    paginate: async () => assert.fail('pagination must not run after a pull request lookup failure'),
  };

  await assert.rejects(
    listPullRequestFiles(github, { owner: 'Gentleman-Programming', repo: 'engram', pullNumber: 1093 }),
    failure,
  );
});
