import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { pathToFileURL } from 'node:url';

const exec = promisify(execFile);
const channels = [
  { name: 'Pi npm latest', url: 'https://registry.npmjs.org/gentle-engram/latest', tag: data => `pi-v${data.version}`, kind: 'pi' },
  { name: 'Go latest stable GitHub Release', url: 'https://api.github.com/repos/Gentleman-Programming/engram/releases/latest', tag: data => data.tag_name, kind: 'go' },
];

export function relevantPath(kind, path) {
  if (path.split('/').includes('docs') || /\.(?:md|mdx|rst|txt)$/i.test(path)) return false;
  if (kind === 'pi') return path.startsWith('plugin/pi/') || path === '.github/workflows/publish-pi.yml';
  if (path === 'tools/cloud-sync-projects.sh' || path === 'tools/cloud-sync-projects.ps1') return true;
  return /^(cmd\/|internal\/|pkg\/|installer\/|go\.mod$|go\.sum$|\.goreleaser\.|\.github\/workflows\/release\.yml$)/.test(path);
}

async function fetchJSON(url) {
  const response = await fetch(url, { headers: { Accept: 'application/json', 'User-Agent': 'engram-release-gap-audit' }, signal: AbortSignal.timeout(15000) });
  if (!response.ok) throw new Error(`HTTP ${response.status} from ${url}`);
  return response.json();
}
async function git(...args) {
  const { stdout } = await exec('git', args, { maxBuffer: 10 * 1024 * 1024 });
  return stdout.trim();
}

export async function audit({ fetchJSON: get = fetchJSON, git: run = git } = {}) {
  const lines = ['## Stable release gap audit', 'Path changes are a release-lag signal, not proof of a fix.'];
  let failed = false;
  try {
    const branch = await run('symbolic-ref', '--quiet', '--short', 'HEAD');
    if (branch !== 'main') throw new Error(`checked-out branch is ${branch || 'unknown'}, not main`);
  } catch (error) {
    return { failed: true, summary: `${lines.join('\n')}\n- **Unable to verify release gap.** Checkout must be branch main: ${String(error.message).replaceAll(/\r|\n/g, ' ')}.\n` };
  }
  for (const channel of channels) {
    try {
      const data = await get(channel.url);
      const tag = channel.tag(data);
      if (channel.kind === 'go' && (data.prerelease !== false || data.draft !== false)) throw new Error('latest release is not confirmed stable');
      if (!new RegExp(channel.kind === 'pi' ? '^pi-v\\d+\\.\\d+\\.\\d+$' : '^v\\d+\\.\\d+\\.\\d+$').test(tag)) throw new Error('missing or malformed stable version/tag');
      await run('rev-parse', '--verify', `refs/tags/${tag}`);
      await run('merge-base', '--is-ancestor', tag, 'HEAD');
      const paths = (await run('diff', '--name-only', `${tag}..HEAD`)).split('\n').filter(Boolean);
      const relevant = paths.filter(path => relevantPath(channel.kind, path));
      if (relevant.length) {
        failed = true;
        lines.push(`- **${channel.name}: gap after ${tag}.** ${relevant.length} relevant path(s) changed on main: ${relevant.slice(0, 8).map(path => `\`${path}\``).join(', ')}${relevant.length > 8 ? ', …' : ''}. Review changes since ${tag}, decide whether a stable release is warranted, then publish through the existing release process.`);
      } else lines.push(`- **${channel.name}:** No relevant changes on main since ${tag}.`);
    } catch (error) {
      failed = true;
      lines.push(`- **${channel.name}: Unable to verify release gap.** ${String(error.message).replaceAll(/\r|\n/g, ' ')}. Check registry/API availability, tag and main history, then rerun the audit.`);
    }
  }
  return { failed, summary: lines.join('\n') + '\n' };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const result = await audit();
  if (process.env.GITHUB_STEP_SUMMARY) {
    const { appendFile } = await import('node:fs/promises');
    await appendFile(process.env.GITHUB_STEP_SUMMARY, result.summary);
  }
  console.log(result.summary);
  if (result.failed) process.exitCode = 1;
}
