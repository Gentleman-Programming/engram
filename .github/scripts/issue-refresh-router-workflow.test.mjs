import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import test from 'node:test';

const root = new URL('../../', import.meta.url);
const read = path => readFileSync(new URL(path, root), 'utf8').replace(/\r\n/g, '\n');
const workflow = () => read('.github/workflows/issue-refresh-router.yml');
const checkout = 'actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6';
const script = 'actions/github-script@ed597411d8f924073f98dfc5c65a23a2325f34cd # v8';

test('router subscribes only to genuine approval-label issue events', () => {
  const yaml = workflow();
  assert.match(yaml, /\non:\n  issues:\n    types: \[labeled, unlabeled\]\n\npermissions: \{\}/);
  assert.match(yaml, /if: \$\{\{ github\.event\.label\.name == 'status:approved' && !github\.event\.issue\.pull_request \}\}/);
  assert.doesNotMatch(yaml, /pull_request_target:|merge_group:|workflow_dispatch:|concurrency:|\brun:/);
});

test('one job owns the minimum repository Actions capability', () => {
  const yaml = workflow();
  assert.deepEqual([...yaml.matchAll(/^  ([\w-]+):$/gm)].map(m => m[1]), ['issues', 'router']);
  assert.match(yaml, /    permissions:\n      contents: read\n      issues: read\n      pull-requests: read\n      actions: write\n/);
  assert.equal((yaml.match(/permissions:/g) || []).length, 2);
  assert.equal((yaml.match(/: write/g) || []).length, 1);
  assert.doesNotMatch(yaml, /checks:|statuses:|secrets\.|token:|continue-on-error:/);
});

test('trusted checkout and static dispatcher call use exact existing pins', () => {
  const yaml = workflow();
  const native = read('.github/workflows/issue-policy.yml');
  for (const pin of [checkout, script]) {
    assert.ok(native.includes(pin));
    assert.ok(yaml.includes(pin));
  }
  assert.equal((yaml.match(/uses:/g) || []).length, 2);
  assert.match(yaml, /ref: \$\{\{ github\.workflow_sha \}\}\n          persist-credentials: false/);
  assert.match(yaml, /    env:\n      TRUSTED_SHA: \$\{\{ github\.workflow_sha \}\}/);
  assert.match(yaml, /retries: 0/);
  assert.match(yaml, /await import\(`\$\{process\.env\.GITHUB_WORKSPACE\}\/\.github\/scripts\/issue-refresh-dispatch\.mjs`\)/);
  assert.match(yaml, /await dispatchIssueApprovalRefresh\(github, context, process\.env\.TRUSTED_SHA\)/);
  assert.match(yaml, /core\.setFailed\(error instanceof Error \? error\.message : String\(error\)\)/);
  const body = yaml.split('          script: |\n')[1];
  assert.ok(body);
  assert.doesNotMatch(body, /\$\{\{|github\.request|github\.rest|process\.env\.(?!GITHUB_WORKSPACE|TRUSTED_SHA)/);
  assert.doesNotMatch(yaml, /github\.event\.(?:issue|pull_request)\.(?:body|title|head)|github\.sha/);
});

test('workflow baselines retain readiness guards and pin original-event identity emission', () => {
  // Integrated fc438498 legacy baselines except the approved readiness guards;
  // native adds only the original-event steps. Normalize to LF for Windows and archives.
  const baseline = {
    '.github/workflows/pr-check.yml': '6c7825060b18790183f584d659778b3649516b7481de934f0b29c02ee3d784a1',
    '.github/workflows/issue-validation.yml': 'd996ffae8e1341390b818e294a1a34a70f1b06f5eabda03cb96c0f065f7115b8',
    '.github/workflows/issue-policy.yml': '1a3091c88771ecc5b51059aad6987d31c97177b48f4522bfdb8963d0c4bc952c',
    '.github/workflows/ci.yml': '9edcc7afba74a6b1554ddc29836d07060ff788e92cd545d0efba2074db0ec8f2',
  };
  for (const [path, expected] of Object.entries(baseline)) {
    const source = path.endsWith('/pr-check.yml') ? read(path).replace(/^                  if \(!Array\.isArray\(issue\.labels\).+\n                    throw new Error\('issue labels are malformed'\);\n                  }\n/m, '').replace(/^                  if \(!Array\.isArray\(issue\.assignees\).+\n                    throw new Error\('issue assignees are malformed'\);\n                  }\n                  if \(issue\.assignees\.length === 0\) \{\n                    errors\.push\(.+\);\n                  }\n/m, '') : read(path);
    assert.equal(createHash('sha256').update(source).digest('hex'), expected, path);
  }
});
