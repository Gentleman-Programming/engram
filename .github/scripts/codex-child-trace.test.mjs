import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const ci = readFileSync(new URL('../workflows/ci.yml', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const windows = ci.match(/^  windows-suite:\n[\s\S]*?(?=^  [a-z][\w-]*:)/m)[0];
test('only the existing Windows suite opts into one Codex child trace', () => {
  const exportLine = 'Add-Content $env:GITHUB_ENV "ENGRAM_CODEX_CHILD_TRACE=$root/codex-child-trace"';
  assert.equal(ci.split(exportLine).length - 1, 1);
  assert.ok(windows.includes(exportLine));
  assert.ok(windows.indexOf(exportLine) < windows.indexOf('& go test -json ./...'));
  assert.equal((windows.match(/& go test -json \.\/\.\.\./g) ?? []).length, 1);
});
test('diagnostic artifact survives test failure without replacing the original Go result', () => {
  const artifact = windows.match(/      - name: Preserve Codex child trace\n[\s\S]*?(?=\n      - name:|$)/)?.[0] ?? '';
  assert.equal(artifact.trimEnd(), `      - name: Preserve Codex child trace
        if: \${{ !cancelled() }}
        continue-on-error: true
        uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7
        with:
          name: codex-child-trace-\${{ github.run_id }}-\${{ github.run_attempt }}
          path: \${{ runner.temp }}/windows-suite/codex-child-trace
          if-no-files-found: warn`);
  assert.match(artifact, /if: \$\{\{ !cancelled\(\) \}\}/);
  assert.match(artifact, /continue-on-error: true/);
  assert.match(artifact, /actions\/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9/);
  assert.match(artifact, /path: \$\{\{ runner\.temp \}\}\/windows-suite\/codex-child-trace/);
  assert.ok(windows.indexOf(artifact) > windows.indexOf('exit $suiteExit'));
});
