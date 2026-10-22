import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const workflow = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const job = workflow.match(/^  windows-unit-tests:\n[\s\S]*?(?=^  [\w-]+:|$(?![\s\S]))/m)?.[0];

test('Windows full suite is independent, observational, and aligned with Linux', () => {
  assert.ok(job, 'missing Windows full-suite job');
  assert.match(job, /name: Windows Unit Tests \(observational\)/);
  assert.match(job, /runs-on: windows-latest/);
  assert.match(job, /if: github.event_name != 'merge_group'/);
  assert.match(job, /ref: \$\{\{ github.sha \}\}/);
  const linux = workflow.match(/^  unit-tests:\n[\s\S]*?(?=^  [\w-]+:)/m)[0];
  assert.equal(job.match(/go-version: "([^"]+)"/)[1], linux.match(/go-version: "([^"]+)"/)[1]);
  assert.match(job, /run: go test \.\/\.\.\./);
  assert.doesNotMatch(job, /continue-on-error|needs:|GOPROXY:|TEMP:|TMP:| -skip| -run|\|\| true/);
});

test('Windows prerequisites use reviewed real executables and fail closed before export or suite', () => {
  const setup = job?.match(/      - name: Select trusted Windows fixture tools\n([\s\S]*?)(?=      - name:)/)?.[1];
  assert.ok(setup, 'missing explicit trusted-tool setup');
  assert.match(setup, /shell: pwsh/);
  assert.ok(setup.includes("$bash = 'C:/Program Files/Git/usr/bin/bash.exe'"));
  assert.ok(setup.includes("$jq = 'C:/ProgramData/chocolatey/lib/jq/tools/jq.exe'"));
  assert.match(setup, /foreach \(\$path in \$required\)/);
  assert.match(setup, /Test-Path -LiteralPath \$path -PathType Leaf/);
  assert.match(setup, /\[System.IO.Path\]::GetExtension\(\$path\) -ne '\.exe'/);
  assert.match(setup, /throw "Missing Windows fixture prerequisite: \$path"/);
  for (const tool of ['cygpath', 'cat', 'dirname', 'chmod']) {
    assert.ok(setup.includes(`C:/Program Files/Git/usr/bin/${tool}.exe`));
  }
  const validation = setup.indexOf('foreach ($path in $required)');
  const execution = setup.indexOf('& $bash --version');
  const exportAt = setup.indexOf('CODEX_TEST_TRUSTED_BASH=$bash');
  assert.ok(validation < execution && execution < exportAt);
  assert.match(setup, /& \$jq --version/);
  assert.match(setup, /if \(\$LASTEXITCODE -ne 0\) \{ throw/);
  assert.match(setup, /CODEX_TEST_TRUSTED_JQ=\$jq/);
  assert.match(setup, /GITHUB_ENV/);
  assert.match(setup, /GITHUB_STEP_SUMMARY/);
  assert.doesNotMatch(setup, /Get-Command|where.exe|Get-ChildItem|choco |Invoke-WebRequest|continue-on-error/);
  assert.ok(job.indexOf(setup) < job.indexOf('Record Windows baseline environment'));
  assert.ok(job.indexOf(setup) < job.indexOf('run: go test ./...'));
});

test('Windows evidence records checkout, toolchain, prerequisites, and actual test outcome', () => {
  assert.ok(job, 'missing Windows full-suite job');
  for (const evidence of ['git rev-parse HEAD', 'go version', 'go env', 'bash --version', 'node --version', '$PSVersionTable', 'steps.suite.outcome']) {
    assert.ok(job.includes(evidence), `missing evidence: ${evidence}`);
  }
  assert.match(job, /id: suite\n\s+run: go test \.\/\.\.\./);
  assert.match(job, /if: always\(\)/);
  assert.match(job, /GITHUB_STEP_SUMMARY/);
});
