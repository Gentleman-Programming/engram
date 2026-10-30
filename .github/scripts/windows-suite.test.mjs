import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const source = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const job = text => text.match(/^  windows-suite:\n[\s\S]*?(?=^  [a-z][\w-]*:|$(?![\s\S]))/m)?.[0] ?? '';

function contract(text) {
  const body = job(text);
  assert.ok(body, 'independent Windows job');
  assert.match(text, /on:\n  pull_request:\n  merge_group:\n    types: \[checks_requested\]\n  push:\n    branches:\n      - main\n/);
  assert.match(text, /run: node --test \.github\/scripts\/windows-suite\.test\.mjs\n/);
  for (const line of ['name: Windows Full Suite (Observational)', 'runs-on: windows-2025',
    'timeout-minutes: 30', 'contents: read', 'GOTOOLCHAIN: local',
    'ENGRAM_PI_SCHEMA_REQUIRE_REAL: "1"', 'shell: pwsh',
    'ref: ${{ github.sha }}', 'persist-credentials: false',
    'go-version: "1.26.10"', 'node-version: "24"',
    'working-directory: plugin/pi', 'run: npm ci --ignore-scripts --legacy-peer-deps',
    'if: failure()', 'if-no-files-found: warn']) {
    assert.ok(body.split('\n').some(value => value.trim() === line), line);
  }
  for (const action of [
    'actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6',
    'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6',
    'actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5',
    'actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7',
  ]) assert.ok(body.includes(`uses: ${action}`), action);
  for (const token of ['git rev-parse HEAD', "(go env GOOS) -ne 'windows'",
    "(go env GOARCH) -ne 'amd64'", "(go env GOVERSION) -ne 'go1.26.10'",
    "(node --version) -notmatch '^v24\\.'", "Get-Command powershell.exe",
    "Get-Command git.exe", "'/(cmd|bin|mingw64/bin|usr/bin)$'", "'usr\\bin\\bash.exe'",
    "$bash -notlike '*\\usr\\bin\\bash.exe'", "Get-Command jq.exe -CommandType Application",
    "$trustedJQ = (Resolve-Path $jq.Source).Path", "[IO.Path]::IsPathFullyQualified($trustedJQ)",
    "[IO.Path]::GetExtension($trustedJQ) -ne '.exe'", 'Test-Path $trustedJQ -PathType Leaf',
    '& $trustedJQ --version', 'if ($LASTEXITCODE -ne 0)',
    'Add-Content $env:GITHUB_ENV "CODEX_TEST_TRUSTED_BASH=$bash"',
    'Add-Content $env:GITHUB_ENV "CODEX_TEST_TRUSTED_JQ=$trustedJQ"',
    '& $bash -lc', 'command -v bash jq curl', 'jq --version', 'curl --version',
    "'GOPATH'", "'GOCACHE'", "'GOMODCACHE'", 'Add-Content $env:GITHUB_ENV',
    '$PSNativeCommandUseErrorActionPreference = $false',
    '$timer = [Diagnostics.Stopwatch]::StartNew()',
    '& go test -json ./... 1> "$evidence/suite.json" 2> "$evidence/stderr.txt"',
    '$suiteExit = $LASTEXITCODE', '$timer.Stop()', 'exit_code=$suiteExit',
    'elapsed_seconds=$($timer.Elapsed.TotalSeconds)', 'exit $suiteExit',
    'path: ${{ runner.temp }}/windows-suite/evidence']) assert.ok(body.includes(token), token);
  for (const key of ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'ENGRAM_DATA_DIR', 'TEMP', 'TMP', 'TMPDIR']) {
    assert.match(body, new RegExp(`(?:^|[ ;])${key} = '(?:home|appdata|localappdata|data|tmp)'`, 'm'), `isolated ${key}`);
  }
  assert.doesNotMatch(body, /continue-on-error|needs:|: write|secrets\.|\|\| true| -p | -parallel| -run | -short| -skip| -tags| -timeout|GOFLAGS|GOOS:|GOARCH:/);
  assert.equal((body.match(/\n\s+if:/g) ?? []).length, 1, 'only failure artifact guard');
  assert.equal(body.split('Add-Content $env:GITHUB_ENV "$key=$value"').length - 1, 2,
    'export both preserved caches and isolated test state');
  assert.ok(body.includes('run: node --test .github/scripts/windows-suite.test.mjs\n'));
  assert.ok(body.indexOf('run: node --test') < body.indexOf('npm ci'));
  assert.ok(body.indexOf('npm ci') < body.indexOf('& go test'));
  assert.ok(body.indexOf('Add-Content $env:GITHUB_ENV') < body.indexOf('npm ci'));
}

test('native observational full-module Windows contract', () => contract(source));

test('installed Git entrypoints resolve to actual usr/bin Bash', {
  skip: process.platform !== 'win32' && 'requires native Windows PowerShell path semantics',
}, t => {
  const body = job(source);
  const root = body.match(/^\s+(\$gitRoot = .+)$/m)?.[1];
  const bash = body.match(/^\s+(\$bash = Join-Path .+)$/m)?.[1];
  assert.ok(root, 'extract current workflow root calculation');
  assert.ok(bash, 'extract current workflow Bash calculation');
  const result = spawnSync('pwsh', ['-NoProfile', '-NonInteractive', '-Command', `
    $ErrorActionPreference = 'Stop'
    @('bin', 'cmd', 'mingw64/bin', 'usr/bin') | ForEach-Object {
      $gitDir = 'C:\\Program Files\\Git\\' + $_.Replace('/', '\\')
      ${root}
      ${bash}
      [pscustomobject]@{ entrypoint = $_; root = $gitRoot; bash = $bash }
    } | ConvertTo-Json -Compress
  `], { encoding: 'utf8', timeout: 10000, windowsHide: true });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, '');
  const cases = JSON.parse(result.stdout);
  t.diagnostic(JSON.stringify(cases));
  assert.deepEqual(cases, ['bin', 'cmd', 'mingw64/bin', 'usr/bin'].map(entrypoint => ({
    entrypoint, root: 'C:/Program Files/Git', bash: 'C:\\Program Files\\Git\\usr\\bin\\bash.exe',
  })));
});

const mutations = [
  ['go test -json ./...', 'go test -json ./internal/setup/...'],
  ['go test -json ./...', 'go test -json -short ./...'],
  ['go test -json ./...', 'go test -json -p 1 ./...'],
  ['exit $suiteExit', 'exit 0'], ['$suiteExit = $LASTEXITCODE', '$suiteExit = 0'],
  ['ENGRAM_PI_SCHEMA_REQUIRE_REAL: "1"', 'ENGRAM_PI_SCHEMA_REQUIRE_REAL: "0"'],
  ['npm ci --ignore-scripts --legacy-peer-deps', 'npm install'],
  ["Get-Command powershell.exe", 'Write-Output powershell.exe'],
  ['command -v bash jq curl', 'command -v bash'],
  ['CODEX_TEST_TRUSTED_BASH=$bash', 'LOST_TRUSTED_BASH=$bash'],
  ['CODEX_TEST_TRUSTED_JQ=$trustedJQ', 'LOST_TRUSTED_JQ=$trustedJQ'],
  ["'usr\\bin\\bash.exe'", "'bin\\bash.exe'"],
  ['$trustedJQ = (Resolve-Path $jq.Source).Path', '$trustedJQ = $bash'],
  ['& $trustedJQ --version', 'Write-Output $trustedJQ'],
  ['if ($LASTEXITCODE -ne 0)', 'if ($false)'],
  ["[IO.Path]::GetExtension($trustedJQ) -ne '.exe'", '$false'],
  ["$bash -notlike '*\\usr\\bin\\bash.exe'", '$false'],
  ["(go env GOOS) -ne 'windows'", "(go env GOOS) -ne 'linux'"],
  ["(go env GOARCH) -ne 'amd64'", "(go env GOARCH) -ne 'arm64'"],
  ['timeout-minutes: 30', 'timeout-minutes: 60'],
  ['contents: read', 'contents: write'],
  ['ref: ${{ github.sha }}', 'ref: main'],
  ['persist-credentials: false', 'persist-credentials: true'],
  ['go-version: "1.26.10"', 'go-version: "1.26.9"'],
  ['node-version: "24"', 'node-version: "22"'],
  ['GOTOOLCHAIN: local', 'GOTOOLCHAIN: auto'],
  ["'GOCACHE'", "'LOST_CACHE'"],
  ['Add-Content $env:GITHUB_ENV "$key=$value"', 'Write-Output "$key=$value"'],
  ['if: failure()', 'if: success()'],
  ['elapsed_seconds=$($timer.Elapsed.TotalSeconds)', 'elapsed_seconds=0'],
  ['2> "$evidence/stderr.txt"', '2> $null'],
  ['1> "$evidence/suite.json"', '1> $null'],
  ['    name: Windows Full Suite', '    continue-on-error: true\n    name: Windows Full Suite'],
  ['    name: Windows Full Suite', '    needs: unit-tests\n    name: Windows Full Suite'],
  ['actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6', 'actions/checkout@v6'],
  ['    name: Windows Full Suite', '    if: false\n    name: Windows Full Suite'],
  ['  merge_group:', '  workflow_dispatch:'],
  ...['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'ENGRAM_DATA_DIR', 'TEMP', 'TMP', 'TMPDIR']
    .map(key => [`${key} = '`, `${key}_LOST = '`]),
];
for (const [before, after] of mutations) {
  test(`reject Windows contract drift: ${before}`, () => {
    const body = job(source);
    const target = before.startsWith('  merge_group:') ? source : body;
    assert.ok(target.includes(before), `mutation target: ${before}`);
    const changed = target.replace(before, after);
    assert.throws(() => contract(target === source ? changed : source.replace(body, changed)));
  });
}
