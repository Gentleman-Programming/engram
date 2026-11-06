import * as fs from 'node:fs';

const profiles = {
  macos: { scope: 'Darwin ARM64 Pi/OpenCode source adapters', title: 'Native macOS adapters' },
  windows: { scope: 'Windows x64 Pi/OpenCode source adapters', title: 'Native Windows adapters' },
};
const profile = Object.hasOwn(profiles, process.argv[2]) && profiles[process.argv[2]];
if (!profile) throw new Error('Usage: node adapter-ci-evidence.mjs <macos|windows>');
const report = { scope: profile.scope, suites: {}, coverage: 'incomplete' };
const required = {
  pi: ['Pi native saves persist under separate host sessions and stop on failed registration',
    'existing CLI verifies real Pi registration without writing',
    'packaged real workflows run all eight tests with the prebuilt fixture binary'],
  opencode: ['V2 inbox prompts stay idempotent and deleted across real server restarts'],
};
for (const [suite, names] of Object.entries(required)) {
  const result = report.suites[suite] = { outcome: process.env[suite.toUpperCase() + '_OUTCOME'], counts: {}, skips: [], complete: false };
  try {
    const raw = fs.readFileSync(process.env.RUNNER_TEMP + '/' + suite + '.tap', 'utf8');
    const lines = raw.trimEnd().split(/\r?\n/), seen = new Set();
    let header = 0, plan, number = 0, records = 0;
    for (const line of lines) {
      if (line === 'TAP version 13') header++;
      const record = line.match(/^\s*(ok|not ok) \d+ - /);
      if (record) {
        records++;
        if (record[1] !== 'ok') throw Error('failed test record');
      }
      const entry = line.match(/^(ok|not ok) (\d+) - (.*?)(?: # SKIP (.*))?$/);
      if (entry) {
        if (!header || entry[1] !== 'ok' || Number(entry[2]) !== ++number) throw Error('invalid top-level result');
        seen.add(entry[3]);
        if (entry[4] !== undefined) result.skips.push({ name: entry[3], reason: entry[4] });
      }
      if (/^1\.\./.test(line)) {
        if (plan !== undefined || !/^1\.\.\d+$/.test(line)) throw Error('invalid plan');
        plan = Number(line.slice(3));
      }
      const count = line.match(/^# (tests|suites|pass|fail|cancelled|skipped|todo) (\d+)$/);
      if (count) {
        if (Object.hasOwn(result.counts, count[1])) throw Error('duplicate counter');
        result.counts[count[1]] = Number(count[2]);
      }
    }
    const c = result.counts;
    if (header !== 1 || !number || plan !== number || Object.keys(c).length !== 7
      || !/^# duration_ms \d+(\.\d+)?$/.test(lines.at(-1))
      || c.tests !== records || c.tests !== c.pass + c.fail + c.cancelled + c.skipped + c.todo
      || !c.pass || c.suites || c.fail || c.cancelled || c.todo || c.skipped !== result.skips.length
      || !names.every(name => seen.has(name))) throw Error('incomplete test execution');
    if (!result.skips.every(s => suite === 'pi'
      && s.name === 'actual Pi hosts retain attribution through compaction, resume and refusal'
      && s.reason === 'opt-in: set ENGRAM_PI_LIFECYCLE_CLI to the installed Pi CLI entry point')) throw Error('unexpected skip');
    result.complete = result.outcome === 'success';
  } catch (error) { result.error = error.message; }
}
if (Object.values(report.suites).every(s => s.complete)) report.coverage = 'complete-with-listed-exclusions';
const json = JSON.stringify(report);
console.log(json);
fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, '## ' + profile.title + '\n\n```json\n' + json.replaceAll('`', '\\u0060') + '\n```\n');
if (report.coverage === 'incomplete') process.exitCode = 1;
