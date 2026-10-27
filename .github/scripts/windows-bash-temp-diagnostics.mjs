import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const aliases = ['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'TEMP', 'TMP', 'TMPDIR'];
const context = ['GITHUB_SHA', 'GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT', 'RUNNER_OS', 'ImageVersion', 'ImageOS'];
const runtime = ['SYSTEMROOT', 'WINDIR', 'OS', 'COMSPEC', 'PATH', 'PATHEXT', 'MSYSTEM',
  'MSYS_NO_PATHCONV', 'MSYS2_ARG_CONV_EXCL', 'MSYS2_ENV_CONV_EXCL'];
const usage = 'Usage: windows-bash-temp-diagnostics.mjs snapshot FILE | collect BASELINE FILE\n';
function select(env, names) {
  const upper = Object.fromEntries(Object.entries(env).map(([key, value]) => [key.toUpperCase(), value]));
  return Object.fromEntries(names.map(key => [key, upper[key.toUpperCase()] ?? null]));
}
const supplied = env => Object.fromEntries(Object.entries(env).filter(([, value]) => value !== null));
export function snapshot(env = process.env) {
  return { version: 1, aliases: select(env, aliases), context: select(env, context), node: process.version };
}
function validate(baseline) {
  if (baseline?.version !== 1 || !aliases.every(key => baseline.aliases?.[key] === null || typeof baseline.aliases?.[key] === 'string')) {
    throw new Error('Invalid diagnostic baseline');
  }
}
const shell = [
  'printf "BASH_VERSION=%s\\n" "$BASH_VERSION"',
  'printf "POSIX_TMP_WINDOWS="',
  'if "$DIAG_CYGPATH" -w /tmp; then printf "MAP_EXIT=0\\n"; else printf "MAP_EXIT=%s\\n" "$?"; fi',
  'if [ -d /tmp ]; then printf "POSIX_TMP_EXISTS=yes\\n"; else printf "POSIX_TMP_EXISTS=no\\n"; fi',
  'for key in TEMP TMP TMPDIR; do',
  '  if [[ -v $key ]]; then',
  '    printf "%s=%s\\n" "$key" "${!key}"',
  '    if "$DIAG_CYGPATH" -u "${!key}"; then printf "%s_MAP_EXIT=0\\n" "$key"; else printf "%s_MAP_EXIT=%s\\n" "$key" "$?"; fi',
  '    if [ -d "${!key}" ]; then printf "%s_EXISTS=yes\\n" "$key"; else printf "%s_EXISTS=no\\n" "$key"; fi',
  '  else printf "%s=<unset>\\n" "$key"; fi',
  'done',
].join('\n');

// Called after the suite: never recreate a missing alias or mapped /tmp.
// execute is the process boundary; contract tests do not start Bash before CI's suite.
export function collect(baseline, parent, env = process.env, execute = spawnSync) {
  validate(baseline);
  const bash = env.CODEX_TEST_TRUSTED_BASH;
  if (!bash || !path.isAbsolute(bash)) throw new Error('Absolute CODEX_TEST_TRUSTED_BASH required');
  const base = { ...supplied(select(env, runtime)), BASH_ENV: '', ENV: '', LC_ALL: 'C',
    DIAG_CYGPATH: path.join(path.dirname(bash), 'cygpath.exe') };
  const original = { version: 1, aliases: select(baseline.aliases, aliases),
    context: select(baseline.context ?? {}, context), node: baseline.node ?? null };
  const result = { version: 1, representativeOnly: true, goTempDirMeasured: false,
    startupFilesDisabled: true, baseline: original, current: snapshot(env), bash, cases: [] };
  const observe = (name, environment) => {
    const native = select(environment, ['TEMP', 'TMP', 'TMPDIR']);
    const directories = Object.fromEntries(Object.entries(native).map(([key, value]) => [key, value === null ? null : existsSync(value)]));
    const child = execute(bash, ['--noprofile', '--norc', '-c', shell], {
      env: environment, encoding: 'utf8', timeout: 10000, windowsHide: true,
    });
    result.cases.push({ name, environment, directories, at: new Date().toISOString(),
      exit: child.status, signal: child.signal, error: child.error?.message ?? null,
      stdout: child.stdout ?? '', stderr: child.stderr ?? '' });
  };
  try {
    // Observe existing aliases before allocating any diagnostic profile.
    observe('original-aliases', { ...base, ...supplied(original.aliases) });
    observe('suite-aliases', { ...base, ...supplied(select(env, aliases)) });
    result.scratch = mkdtempSync(path.join(parent, 'bash-temp-'));
    const fixture = { ...base, PATH: '', MSYS_NO_PATHCONV: '1' };
    for (const [dir, keys] of Object.entries({ home: ['HOME', 'USERPROFILE'],
      config: ['APPDATA', 'XDG_CONFIG_HOME', 'ENGRAM_CONFIG_DIR', 'CURL_HOME', 'CODEX_HOME', 'CLAUDE_CONFIG_DIR'],
      data: ['LOCALAPPDATA', 'XDG_DATA_HOME', 'ENGRAM_DATA_DIR'], tmp: ['TEMP', 'TMP', 'TMPDIR'] })) {
      const value = path.join(result.scratch, dir);
      mkdirSync(value);
      for (const key of keys) fixture[key] = value;
    }
    observe('fixture-like-no-gate', fixture);
  } catch (error) {
    result.setupError = error.message;
  } finally {
    if (result.scratch) rmSync(result.scratch, { recursive: true, force: true });
    result.scratchRemoved = !result.scratch || !existsSync(result.scratch);
  }
  return result;
}
function main(args) {
  if (args.length === 1 && args[0] === '--help') { process.stdout.write(usage); return; }
  if (!(args[0] === 'snapshot' && args.length === 2) && !(args[0] === 'collect' && args.length === 3)) {
    process.stderr.write(usage); process.exitCode = 2; return;
  }
  try {
    if (args[0] === 'snapshot') {
      mkdirSync(path.dirname(args[1]), { recursive: true });
      writeFileSync(args[1], JSON.stringify(snapshot(), null, 2) + '\n');
    } else {
      const result = collect(JSON.parse(readFileSync(args[1], 'utf8')), path.dirname(args[2]));
      writeFileSync(args[2], JSON.stringify(result, null, 2) + '\n');
      if (result.setupError || result.cases.length !== 3 || result.cases.some(c => c.exit !== 0 || c.error) || !result.scratchRemoved) process.exitCode = 1;
    }
  } catch (error) { process.stderr.write(error.message + '\n'); process.exitCode = 1; }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main(process.argv.slice(2));
