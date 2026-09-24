import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { audit, relevantPath } from './release-gap-audit.mjs';

const fixtures = {
  'https://registry.npmjs.org/gentle-engram/latest': { version: '0.1.15' },
  'https://api.github.com/repos/Gentleman-Programming/engram/releases/latest': { tag_name: 'v2.1.0', prerelease: false, draft: false },
};
function harness({ files = [], metadata = fixtures, tags = ['pi-v0.1.15', 'v2.1.0'], branch = 'main' } = {}) {
  const calls = [];
  return {
    calls,
    fetchJSON: async url => { if (!(url in metadata)) throw new Error('HTTP 503'); return metadata[url]; },
    git: async (...args) => {
      calls.push(args);
      if (args[0] === 'symbolic-ref') return branch;
      if (args[0] === 'rev-parse') return tags.includes(args[2]?.replace('refs/tags/', '')) ? 'abc' : (() => { throw new Error('tag missing'); })();
      if (args[0] === 'merge-base') return '';
      if (args[0] === 'diff') return files.join('\n');
      throw new Error('unexpected git invocation');
    },
  };
}

test('docs-only changes inside both release trees are not a gap', async () => {
  const result = await audit(harness({ files: [
    'docs/README.md', '.github/workflows/ci.yml',
    'plugin/pi/README.md', 'plugin/pi/docs/usage.md',
    'internal/store/README.md', 'cmd/engram/docs/setup.md',
  ] }));
  assert.equal(result.failed, false);
  assert.match(result.summary, /No relevant changes/);
});
test('Pi and Go relevant changes report actionable gaps without claiming fixes', async () => {
  const fixture = harness({ files: ['plugin/pi/index.ts', 'internal/store/store.go'] });
  const result = await audit(fixture);
  assert.equal(result.failed, true);
  assert.match(result.summary, /pi-v0\.1\.15/);
  assert.match(result.summary, /v2\.1\.0/);
  assert.match(result.summary, /Review changes/);
  assert.doesNotMatch(result.summary, /fix detected/i);
  assert.deepEqual(fixture.calls.filter(([command]) => command === 'merge-base'), [
    ['merge-base', '--is-ancestor', 'pi-v0.1.15', 'HEAD'],
    ['merge-base', '--is-ancestor', 'v2.1.0', 'HEAD'],
  ]);
  assert.deepEqual(fixture.calls.filter(([command]) => command === 'diff'), [
    ['diff', '--name-only', 'pi-v0.1.15..HEAD'],
    ['diff', '--name-only', 'v2.1.0..HEAD'],
  ]);
});
test('non-main checkout fails closed before comparing release tags', async () => {
  const fixture = harness({ branch: 'feature', files: ['internal/store/store.go'] });
  const result = await audit(fixture);
  assert.equal(result.failed, true);
  assert.match(result.summary, /Unable to verify.*main/s);
  assert.equal(fixture.calls.some(([command]) => command === 'diff'), false);
  assert.equal(fixture.calls.some(([command]) => command === 'merge-base'), false);
});
test('workflow explicitly checks out main for manual dispatch', () => {
  const workflow = readFileSync(new URL('../workflows/release-gap-audit.yml', import.meta.url), 'utf8');
  assert.match(workflow, /with:\s*\n\s*ref: main\s*\n\s*fetch-depth: 0/);
});
test('missing tag and unavailable or malformed metadata fail visibly', async () => {
  for (const options of [
    { tags: ['v2.1.0'] },
    { metadata: {} },
    { metadata: { ...fixtures, 'https://registry.npmjs.org/gentle-engram/latest': { version: '../evil' } } },
  ]) {
    const result = await audit(harness(options));
    assert.equal(result.failed, true);
    assert.match(result.summary, /Unable to verify/);
  }
});
test('only runtime and build inputs classify, not similarly named docs', () => {
  assert.equal(relevantPath('pi', 'plugin/pi/src/index.ts'), true);
  assert.equal(relevantPath('pi', 'docs/plugin/pi/index.ts'), false);
  assert.equal(relevantPath('go', 'go.mod'), true);
  assert.equal(relevantPath('go', 'internal/store/store.go'), true);
  assert.equal(relevantPath('go', 'plugin/pi/index.ts'), false);
  assert.equal(relevantPath('go', 'tools/cloud-sync-projects.sh'), true);
  assert.equal(relevantPath('go', 'tools/cloud-sync-projects.ps1'), true);
  assert.equal(relevantPath('go', 'tools/unrelated.sh'), false);
});
