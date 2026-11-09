import assert from 'node:assert/strict';
import test from 'node:test';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import {
  findTransientArtifacts,
  isTransientArtifactPath,
  listPullRequestFiles,
} from './transient-artifacts.mjs';

test('checkout ignores root release output without hiding source or nested dist directories', () => {
  const fixture = mkdtempSync(join(tmpdir(), 'engram-release-ignore-'));
  try {
    const emptyConfig = join(fixture, 'empty-config');
    const emptyTemplate = join(fixture, 'empty-template');
    writeFileSync(emptyConfig, '');
    mkdirSync(emptyTemplate);
    const env = Object.fromEntries(
      Object.entries(process.env).filter(([key]) => !key.toUpperCase().startsWith('GIT_')),
    );
    Object.assign(env, {
      GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_GLOBAL: emptyConfig,
      GIT_CONFIG_SYSTEM: emptyConfig,
    });
    const repository = join(fixture, 'repository');
    mkdirSync(repository);
    const git = (args, status, stdout) => {
      const result = spawnSync('git', [
        '-c', `core.excludesFile=${emptyConfig}`,
        '-c', `core.hooksPath=${emptyTemplate}`,
        ...args,
      ], { cwd: repository, env, encoding: 'utf8', timeout: 5000 });
      assert.ifError(result.error);
      assert.equal(result.status, status, `git ${args.join(' ')}: ${result.stderr}`);
      assert.equal(result.stderr, '');
      assert.equal(result.stdout, stdout);
    };
    git(['init', '--quiet', `--template=${emptyTemplate}`], 0, '');
    copyFileSync(new URL('../../.gitignore', import.meta.url), join(repository, '.gitignore'));
    const ignored = ['dist/metadata.json', 'dist/nested/output.txt', 'engram', 'tool.exe'];
    const visible = ['source-marker.txt', 'subproject/dist/source-marker.txt'];
    for (const path of [...ignored, ...visible]) {
      mkdirSync(join(repository, path, '..'), { recursive: true });
      writeFileSync(join(repository, path), 'fixture\n');
    }
    for (const path of ignored) {
      git(['check-ignore', '--', path], 0, `${path}\n`);
    }
    for (const path of visible) {
      git(['check-ignore', '--', path], 1, '');
    }
    git(['status', '--porcelain=v1', '--untracked-files=all'], 0,
      '?? .gitignore\n?? source-marker.txt\n?? subproject/dist/source-marker.txt\n');
  } finally {
    rmSync(fixture, { recursive: true, force: true });
  }
});

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
