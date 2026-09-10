const generatedAgentLinkDirectories = [
  ['.claude', 'skills'],
  ['.codex', 'skills'],
  ['.github', 'skills'],
  ['.gemini', 'skills'],
];

const rootBinaryPaths = new Set([
  'engram',
  'cmd/engram/main',
  'cmd/engram/gentle-creation',
  'cmd/engram/engram',
]);

function normalizePath(filePath) {
  return filePath.replaceAll('\\', '/').replace(/^\.\//, '');
}

function pathSegments(filePath) {
  return normalizePath(filePath).split('/');
}

function hasDirectory(filePath, directory) {
  return pathSegments(filePath).includes(directory);
}

function hasRootSubpath(filePath, subpath) {
  const segments = pathSegments(filePath);
  return subpath.every((segment, index) => segments[index] === segment);
}

function fileName(filePath) {
  const segments = pathSegments(filePath);
  return segments.at(-1);
}

export function transientArtifactReason(filePath) {
  const normalizedPath = normalizePath(filePath);
  const name = fileName(normalizedPath);

  if (hasDirectory(normalizedPath, '.atl')) {
    return '.atl artifact';
  }
  if (generatedAgentLinkDirectories.some(directory => hasRootSubpath(normalizedPath, directory))) {
    return 'generated agent-link directory';
  }
  if (hasDirectory(normalizedPath, 'engram-dev')) {
    return 'engram-dev artifact';
  }
  if (name === '.release-notes-beta.md') {
    return 'release-notes beta artifact';
  }
  if (name.endsWith('.db') || name.endsWith('.db-wal') || name.endsWith('.db-shm')) {
    return 'local database';
  }
  if (name === 'engram-export.json') {
    return 'local export';
  }
  if (rootBinaryPaths.has(normalizedPath) || name.endsWith('.exe')) {
    return 'binary';
  }
  if (name === '.DS_Store' || name === 'Thumbs.db') {
    return 'OS metadata';
  }
  if (hasDirectory(normalizedPath, '.idea') || hasDirectory(normalizedPath, '.vscode')) {
    return 'editor metadata';
  }
  if (name.endsWith('.swp') || name.endsWith('.swo') || name.endsWith('~')) {
    return 'editor or backup file';
  }

  return null;
}

export function isTransientArtifactPath(filePath) {
  return transientArtifactReason(filePath) !== null;
}

export function findTransientArtifacts(files) {
  return files
    .filter(file => file.status !== 'removed')
    .map(file => ({ ...file, reason: transientArtifactReason(file.filename) }))
    .filter(file => file.reason !== null);
}

export async function listPullRequestFiles(github, { owner, repo, pullNumber }) {
  return github.paginate(github.rest.pulls.listFiles, {
    owner,
    repo,
    pull_number: pullNumber,
    per_page: 100,
  });
}
