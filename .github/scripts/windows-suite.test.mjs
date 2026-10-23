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

test('Windows CURL selection is explicit, reviewed, and validated before export', () => {
  const setup = job.match(/      - name: Select trusted Windows fixture tools\n([\s\S]*?)(?=      - name:)/)[1];
  assert.ok(setup.includes("$curl = 'C:/Windows/System32/curl.exe'"));
  assert.match(setup, /\$required = @\([\s\S]*?\$curl/);
  assert.match(setup, /\$curlVersion = & \$curl --version\n\s+if \(\$LASTEXITCODE -ne 0\) \{ throw/);
  assert.match(setup, /"Trusted curl: \$curl", \$curlVersion/);
  const validation = setup.indexOf('foreach ($path in $required)');
  const version = setup.indexOf('& $curl --version');
  const exported = setup.indexOf('CODEX_TEST_TRUSTED_CURL=$curl');
  assert.ok(validation < version && version < exported);
  assert.match(setup, /CODEX_TEST_TRUSTED_CURL=\$curl/);
  assert.doesNotMatch(setup, /mingw64|Get-Command|where\.exe|Invoke-WebRequest|choco /);
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


test('Windows Bash temp diagnostics are bounded, read-only, and precede the unchanged suite', () => {
  const diagnosticStep = '      - name: Diagnose Git Bash temporary directory';
  assert.ok(job.includes(diagnosticStep), 'missing read-only Bash temp diagnostic');
  assert.match(job, /name: Diagnose Git Bash temporary directory\n\s+shell: pwsh\n\s+run: \.\/\.github\/scripts\/windows-bash-tmp-diagnostic\.ps1/);
  assert.ok(job.indexOf('Select trusted Windows fixture tools') < job.indexOf(diagnosticStep));
  assert.ok(job.indexOf(diagnosticStep) < job.indexOf('run: go test ./...'));
  const diagnostic = readFileSync(new URL('./windows-bash-tmp-diagnostic.ps1', import.meta.url), 'utf8');
  for (const required of ['RUNNER_TEMP', '.Environment.Clear()', "'--noprofile'", "'--norc'", "'-p'", 'native-bootstrap', 'posix-launcher', 'native-temp-posix-home', 'WaitForExit(10000)', '.Kill($true)', 'ReadToEndAsync()', 'exit_code', 'timed_out', 'stdout', 'stderr', 'tmp_native_path', 'Get-Item -LiteralPath', 'GITHUB_STEP_SUMMARY']) {
    assert.ok(diagnostic.includes(required), `missing diagnostic invariant: ${required}`);
  }
  assert.doesNotMatch(diagnostic, /New-Item|CreateDirectory|mkdir|Set-Item|SetEnvironmentVariable|GITHUB_ENV|Invoke-WebRequest|choco |Set-Content|Add-Content|Remove-Item/);
  assert.doesNotMatch(diagnostic, /\$env:[\w]+\s*=/);
  assert.match(job, /id: suite\n\s+run: go test \.\/\.\.\./);
});
