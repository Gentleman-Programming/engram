import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

const script = path.resolve('.github/scripts/windows-tmp-diagnostic.mjs');
const source = fs.readFileSync('plugin/codex_url_intent_test.go', 'utf8');
const workflow = () => { try { return fs.readFileSync('.github/workflows/windows-tmp-diagnostic.yml', 'utf8').replaceAll('\r\n', '\n'); } catch { return ''; } };
const target = 'TestCodexShellNativeURLIntent/post-compaction.sh/___';
function fixture(text = source) {
  fs.mkdirSync('odd/tasks', { recursive: true });
  const root = fs.mkdtempSync(path.resolve('odd/tasks/1891-diag-test-'));
  fs.mkdirSync(path.join(root, 'plugin'));
  const file = path.join(root, 'plugin/codex_url_intent_test.go');
  fs.writeFileSync(file, text);
  return { root, file };
}

test('public instrumenter preserves source and original test channels/budgets', () => {
  const f = fixture();
  const r = spawnSync(process.execPath, [script], { cwd: f.root, encoding: 'utf8' });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stderr, '');
  const report = JSON.parse(r.stdout);
  assert.equal(report.target, target);
  const candidate = fs.readFileSync(f.file, 'utf8').replaceAll('\r\n', '\n');
  const original = source.replaceAll('\r\n', '\n');
  for (const text of ['if err != nil || diagnostic.Len() != 0 {', '5*time.Second', 'cmd.WaitDelay = time.Second', 'cmd.Stdout, cmd.Stderr = &out, &diagnostic', 'hook) [ -f "$script" ] || exit 98; exec "$trusted_bash" --noprofile --norc -p "$script" ;;']) assert.ok(candidate.includes(text));
  const additions = report.additions;
  let recovered = candidate;
  for (const addition of additions) recovered = recovered.replace(addition, '');
  assert.equal(recovered, original);
  assert.ok(candidate.includes('codex_tmp_probe entry >&9 2>&9'));
  assert.ok(candidate.includes('codex_tmp_probe before-hook-exec >&9 2>&9'));
  assert.ok(candidate.includes('[[ -d /tmp ]]'));
  assert.ok(candidate.indexOf('exec 9>&-') < candidate.indexOf('case "$1" in'));
  assert.ok(candidate.includes('t.Logf("TMP_DIAGNOSTIC_BEGIN %s TMP_DIAGNOSTIC_END", probe)'));
});

test('source drift is rejected before any stored bytes change', () => {
  const f = fixture(source + '\n// drift\n');
  const before = fs.readFileSync(f.file);
  const r = spawnSync(process.execPath, [script], { cwd: f.root, encoding: 'utf8' });
  assert.equal(r.status, 1);
  assert.equal(r.stdout, '');
  assert.equal(r.stderr, 'Diagnostic source mismatch; refusing to instrument\n');
  assert.deepEqual(fs.readFileSync(f.file), before);
  assert.deepEqual(fs.readdirSync(path.join(f.root, 'plugin')), ['codex_url_intent_test.go']);
});

test('new job reuses prerequisites without modifying or replacing the full suite', () => {
  const yml = workflow();
  const baseline = fs.readFileSync('.github/workflows/ci.yml', 'utf8').replaceAll('\r\n', '\n');
  const prep = text => text.match(/      - name: Verify native prerequisites and isolate test state\n[\s\S]*?(?=\n      - name: Install mandatory real Pi schema dependency)/)?.[0];
  assert.ok(prep(yml));
  assert.equal(prep(yml), prep(baseline));
  assert.match(yml, /pull_request:/);
  assert.doesNotMatch(yml, /pull_request_target:|workflow_dispatch:|continue-on-error:|checks: write|contents: write|secrets\./);
  assert.match(yml, /runs-on: windows-2025/);
  assert.match(yml, /persist-credentials: false/);
  assert.match(yml, /- name: Install mandatory real Pi schema dependency/);
  assert.match(yml, /node \.github\/scripts\/windows-tmp-diagnostic\.mjs/);
  assert.match(baseline, /& go test -json \.\/\.\.\./);
  assert.match(yml, /if: always\(\)/);
});

test('native PowerShell preserves Go exit/streams and rejects zero-test evidence', { skip: process.platform !== 'win32' }, async t => {
  const block = workflow().match(/      - name: Run one diagnostic case\n        run: \|\n([\s\S]*?)(?=\n      - name:)/)?.[1];
  assert.ok(block, 'public workflow execution block');
  const body = block.split('\n').map(line => line.startsWith('          ') ? line.slice(10) : line).join('\n');
  const build = fixture();
  fs.writeFileSync(path.join(build.root, 'fixture.cs'), `using System;
using System.IO;
using System.Text;
public static class DiagnosticGoFixture {
  public static int Main(string[] args) {
    string root = Environment.GetEnvironmentVariable("DIAGNOSTIC_GO_ROOT");
    string[] encoded = new string[args.Length];
    for (int i = 0; i < args.Length; i++) encoded[i] = Convert.ToBase64String(Encoding.UTF8.GetBytes(args[i]));
    File.WriteAllLines(Path.Combine(root, "argv.base64"), encoded);
    Console.Write(File.ReadAllText(Path.Combine(root, "records.jsonl")));
    Console.Error.WriteLine("fixture-go-stderr");
    return Int32.Parse(Environment.GetEnvironmentVariable("DIAGNOSTIC_GO_EXIT"));
  }
}`);
  fs.writeFileSync(path.join(build.root, 'build.ps1'), "$ErrorActionPreference = 'Stop'\nAdd-Type -Path './fixture.cs' -OutputAssembly './go.exe' -OutputType ConsoleApplication\n");
  const compiler = spawnSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-File', path.join(build.root, 'build.ps1')], { cwd: build.root, encoding: 'utf8', timeout: 5000 });
  assert.equal(compiler.status, 0, compiler.stderr);
  assert.equal(compiler.stderr, '');
  for (const scenario of [{ code: 1, valid: true }, { code: 0, valid: true }, { code: 0, valid: false }]) {
    await t.test(JSON.stringify(scenario), () => {
      const f = fixture();
      fs.mkdirSync(path.join(f.root, 'bin'));
      fs.mkdirSync(path.join(f.root, 'windows-suite/evidence'), { recursive: true });
      const records = scenario.valid ? [{ Action: 'run', Test: target }, { Action: 'output', Test: target, Output: 'TMP_DIAGNOSTIC_BEGIN phase=entry phase=before-hook-exec TMP_DIAGNOSTIC_END' }, { Action: scenario.code ? 'fail' : 'pass', Test: target }] : [];
      fs.writeFileSync(path.join(f.root, 'records.jsonl'), records.map(record => JSON.stringify(record)).join('\n') + '\n');
      fs.copyFileSync(path.join(build.root, 'go.exe'), path.join(f.root, 'bin/go.exe'));
      fs.writeFileSync(path.join(f.root, 'run.ps1'), body);
      const r = spawnSync('pwsh.exe', ['-NoProfile', '-NonInteractive', '-File', path.join(f.root, 'run.ps1')], { cwd: f.root, env: { ...process.env, RUNNER_TEMP: f.root, DIAGNOSTIC_GO_ROOT: f.root, DIAGNOSTIC_GO_EXIT: String(scenario.code), PATH: path.join(f.root, 'bin') + ';' + process.env.PATH }, encoding: 'utf8', timeout: 5000 });
      assert.equal(r.status, scenario.code || (scenario.valid ? 0 : 1), r.stderr);
      const decode = file => { const bytes = fs.readFileSync(file); return bytes[0] === 255 && bytes[1] === 254 ? bytes.toString('utf16le').slice(1) : bytes.toString('utf8'); };
      const evidence = path.join(f.root, 'windows-suite/evidence');
      assert.match(decode(path.join(evidence, 'stderr.txt')), /^fixture-go-stderr\r?\n$/);
      assert.ok(r.stdout.includes('fixture-go-stderr'));
      assert.match(decode(path.join(evidence, 'result.txt')), new RegExp('go_exit=' + scenario.code));
      assert.match(decode(path.join(evidence, 'result.txt')), new RegExp('capture_valid=' + (scenario.valid ? 'True' : 'False')));
      assert.deepEqual(fs.readFileSync(path.join(f.root, 'argv.base64'), 'utf8').trim().split(/\r?\n/).map(arg => Buffer.from(arg, 'base64').toString('utf8')), ['test', '-json', '-count=1', '-run', '^TestCodexShellNativeURLIntent$/^post-compaction[.]sh$/^___$', './plugin']);
      if (!scenario.valid) assert.ok(r.stderr.includes('Focused diagnostic capture is incomplete'));
    });
  }
});
