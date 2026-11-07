// Temporary diagnostic PR only. Run in its isolated job checkout, not a working tree.
import fs from 'node:fs';
import crypto from 'node:crypto';

try {
  const file = 'plugin/codex_url_intent_test.go';
  const original = fs.readFileSync(file, 'utf8');
  const normalized = original.replaceAll('\r\n', '\n');
  const blob = crypto.createHash('sha1').update(`blob ${Buffer.byteLength(normalized)}`).update(Buffer.from([0])).update(normalized).digest('hex');
  if (blob !== '4f72192f319c5ea71ee1fd8409a18794b7be9128') throw new Error('Diagnostic source mismatch; refusing to instrument');
  const log = [
    '\tif probe, probeErr := os.ReadFile(filepath.Join(f.root, "tmp-diagnostic.log")); probeErr == nil {',
    '\t\tt.Logf("TMP_DIAGNOSTIC_BEGIN %s TMP_DIAGNOSTIC_END", probe)',
    '\t} else {',
    '\t\tt.Logf("TMP_DIAGNOSTIC_UNAVAILABLE: %v", probeErr)',
    '\t}'
  ].join('\n') + '\n';
  const shell = [
    '# Diagnostic output only; original child stdout/stderr remain connected to Go.',
    'exec 9>./tmp-diagnostic.log',
    'codex_tmp_probe() {',
    ' local key value',
    ' printf \'phase=%s bash_pid=%s parent_pid=%s bash=%q version=%q time=%s\\n\' "$1" "${BASHPID-$$}" "$PPID" "$BASH" "$BASH_VERSION" "${EPOCHREALTIME-unknown}"',
    ' printf \'physical_cwd=\'; pwd -P',
    ' for key in TMPDIR TMP TEMP; do',
    '  value=${!key-}',
    '  printf \'%s=%q is_directory=\' "$key" "$value"',
    '  if [[ -d "$value" ]]; then printf \'true\\n\'; else printf \'false\\n\'; fi',
    ' done',
    ' printf \'msys_tmp_is_directory=\'',
    ' if [[ -d /tmp ]]; then printf \'true\\n\'; else printf \'false\\n\'; fi',
    ' if [[ -n "$converter" ]]; then',
    '  printf \'msys_tmp_windows=\'',
    '  "$converter" -w -- /tmp',
    ' fi',
    '}',
    'codex_tmp_probe entry >&9 2>&9 || :'
  ].join('\n') + '\n';
  const before = 'codex_tmp_probe before-hook-exec >&9 2>&9 || :\nexec 9>&-\n';
  const additions = [log, shell, before];
  const anchors = ['\terr := cmd.Run()\n', 'set -euo pipefail\n', '[ ! -f "$INTENT_RAW_CWD/.engram/manifest.json" ] || exit 98\n'];
  let candidate = normalized;
  for (let i = 0; i < anchors.length; i++) {
    if (candidate.split(anchors[i]).length !== 2) throw new Error('Diagnostic anchor mismatch; refusing to instrument');
    candidate = candidate.replace(anchors[i], () => anchors[i] + additions[i]);
  }
  const output = original.includes('\r\n') ? candidate.replaceAll('\n', '\r\n') : candidate;
  fs.writeFileSync(file, output);
  console.log(JSON.stringify({ target: 'TestCodexShellNativeURLIntent/post-compaction.sh/___', sourceBlob: blob, additions }));
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
