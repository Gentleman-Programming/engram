import { execFileSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';

const plugin = 'plugin/claude-code/.claude-plugin/plugin.json';
const marketplace = '.claude-plugin/marketplace.json';
const paths = [plugin, marketplace];

function version(text, path) {
  if (typeof text !== 'string') throw new Error(`missing ${path}`);
  let parsed;
  try { parsed = JSON.parse(text); } catch { throw new Error(`malformed ${path}`); }
  const value = path === plugin ? parsed?.version : parsed?.plugins?.find((entry) => entry?.name === 'engram')?.version;
  if (typeof value !== 'string' || !/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(value)) {
    throw new Error(`invalid version in ${path}`);
  }
  return value;
}

function greater(a, b) {
  const left = a.split('.').map(BigInt);
  const right = b.split('.').map(BigInt);
  for (let i = 0; i < 3; i++) {
    if (left[i] !== right[i]) return left[i] > right[i];
  }
  return false;
}

export function checkPluginVersion({ changes, base, head }) {
  let touched = false;
  for (const change of changes) {
    const fields = change.split('\t');
    if (!/^[ACDMRTUXB][0-9]*$/.test(fields[0]) || fields.length !== (/^[RC]/.test(fields[0]) ? 3 : 2) || fields.slice(1).some((p) => !p)) {
      throw new Error('malformed git change record');
    }
    touched ||= fields.slice(1).some((p) => p.startsWith('plugin/claude-code/'));
  }
  if (!touched) return;
  const before = paths.map((path) => version(base[path], path));
  const after = paths.map((path) => version(head[path], path));
  if (before[0] !== before[1]) throw new Error('base plugin versions disagree');
  if (after[0] !== after[1]) throw new Error('head plugin versions disagree');
  if (!greater(after[0], before[0]) || !greater(after[1], before[1])) {
    throw new Error('both plugin versions must advance');
  }
}

function git(...args) { return execFileSync('git', args, { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 }); }

function changesBetween(base, head) {
  const fields = git('diff', '--name-status', '-z', '--no-ext-diff', base, head, '--').split('\0');
  if (fields.pop() !== '') throw new Error('malformed git diff output');
  const changes = [];
  for (let i = 0; i < fields.length;) {
    const status = fields[i++];
    const count = /^[RC]/.test(status) ? 2 : 1;
    if (i + count > fields.length) throw new Error('malformed git diff output');
    changes.push([status, ...fields.slice(i, i + count)].join('\t'));
    i += count;
  }
  return changes;
}

function manifestAt(ref, path) {
  try { return git('show', `${ref}:${path}`); } catch { return undefined; }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    const [base, head] = process.argv.slice(2);
    if (!base || !head || !/^[a-f0-9]{40}$/.test(base) || !/^[a-f0-9]{40}$/.test(head)) throw new Error('expected base and head commit SHAs');
    const changes = changesBetween(base, head);
    const load = (ref) => Object.fromEntries(paths.map((path) => [path, manifestAt(ref, path)]));
    checkPluginVersion({ changes, base: load(base), head: load(head) });
    console.log('Claude plugin version guard passed');
  } catch (error) {
    console.error(`Claude plugin version guard failed: ${error.message}`);
    process.exitCode = 1;
  }
}
