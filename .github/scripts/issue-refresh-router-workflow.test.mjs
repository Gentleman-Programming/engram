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
  // Integrated fc438498 baselines plus the approved #1501 reference port/readiness guards;
  // native adds only the original-event steps. CI includes the independently/native-approved
  // eight-line Go bootstrap for #1872 in 07a234c59ebeb01be536e8b1d973a0f61ff4bf2b,
  // plus the approved #1994 TypeBox test bootstrap in f65380cc79ffa6fe63d4594b1e5341733d51f288.
  // #2030 adds dormant boundaries/source pins to native/legacy workflows only.
  // CI retains main's selected-proof bootstrap plus #2043's mandatory selected-Pi source test.
  // #1810 adds the dormant authenticated fork configuration path; other guards stay exact.
  // #2068 adds only the existing non-mutating tidy check to required Unit Tests after Go setup.
  // #2071 adds an isolated, read-only jq 1.6 compatibility job; existing CI jobs stay exact.
  // #2019 adds independent read-only scanner jobs and their policy-test step only.
  // #2096 adds only guarded workflow-level concurrency; existing CI jobs stay exact.
  // #2107 updates the nine explicit CI toolchain pins to Go 1.26.10; job guards stay exact.
  // #2017 adds only the isolated PostgreSQL job and its policy-test step; existing guards stay exact.
  // #2018 adds the independent Linux race job with its measured 20m per-binary budget;
  // existing jobs stay exact.
  // #2100/#2055 add approved Node24 unit bootstrap and policy regression registration only.
  // #2144 adds only explicit technical-contract discovery alongside the unchanged issue wildcard.
  // Normalize to LF for Windows and archives; retain strict full-file hash comparisons.
  const baseline = {
    '.github/workflows/pr-check.yml': '33259c75e4f7dfdfca3143fc5efa0521a8ca289f06eca0d3efb429fbad7565bf',
    '.github/workflows/issue-validation.yml': 'd996ffae8e1341390b818e294a1a34a70f1b06f5eabda03cb96c0f065f7115b8',
    '.github/workflows/issue-policy.yml': '5f8bfbd9ab354f6b60d3ef35d30d5f4ee43a97eb83f61d8e0a955da37947adfd',
    '.github/workflows/ci.yml': 'df6226a5c79a05ac0987486b7f255ca7b1a91ee63750a52b70b18525d6046120',
  };
  for (const [path, expected] of Object.entries(baseline)) {
    const source = path.endsWith('/pr-check.yml') ? read(path).replace(/^                  if \(!Array\.isArray\(issue\.labels\).+\n                    throw new Error\('issue labels are malformed'\);\n                  }\n/m, '').replace(/^                  if \(!Array\.isArray\(issue\.assignees\).+\n                    throw new Error\('issue assignees are malformed'\);\n                  }\n                  if \(issue\.assignees\.length === 0\) \{\n                    errors\.push\(.+\);\n                  }\n/m, '') : read(path);
    assert.equal(createHash('sha256').update(source).digest('hex'), expected, path);
  }
});
