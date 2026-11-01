import fs from 'node:fs';

const prefix = 'github.com/Gentleman-Programming/engram/v3/';
const profiles = {
  'postgres-measurements': [
    ...['chunks', 'mutations'].flatMap(axis => [10, 100, 1000].map(n =>
      ['internal/cloud/cloudstore', `TestDashboardReadModelPostgresCounts/${axis}/H${n}`])),
    ['internal/cloud/cloudstore', 'TestDashboardReadModelPostgresControlsAndRecreation'],
    ['internal/cloud/cloudserver', 'TestPausePGPullSnapshotPagination'],
    ...[1, 10, 100].map(n => ['internal/cloud/cloudserver', `TestMutationPausePostgresQueryCount/B100/D${n}`]),
  ],
  'windows-recovery': [['cmd/engram', 'TestHookManagedLocalPublicRecovery']],
  'unsupported-node': [['tools/release-candidate', 'TestArtifactCoreUnsupportedNode']],
};
const usage = 'Usage: node .github/scripts/prerequisite-coverage.mjs PROFILE EVENTS.json GO_EXIT';
if (process.argv.length === 3 && process.argv[2] === '--help') {
  console.log(usage);
} else {
  try {
    const [profile, file, exit] = process.argv.slice(2);
    if (process.argv.length !== 5 || !Object.hasOwn(profiles, profile)) throw new Error(usage);
    if (exit !== '0') throw new Error(`Go exited with ${exit}`);
    const packages = new Map(), tests = new Map();
    const actions = new Set(['start', 'run', 'pause', 'cont', 'pass', 'skip', 'fail', 'output', 'bench', 'build-output', 'build-fail']);
    for (const line of fs.readFileSync(file, 'utf8').split('\n')) {
      if (!line.trim()) continue;
      const e = JSON.parse(line);
      if (!e || Array.isArray(e) || !actions.has(e.Action)) throw new Error('invalid Go event');
      if (e.Action === 'fail' || e.Action === 'build-fail') throw new Error('Go failure');
      if (e.Action === 'build-output') continue;
      if (typeof e.Package !== 'string' || !e.Package || (e.Test !== undefined && (typeof e.Test !== 'string' || !e.Test))) throw new Error('invalid event identity');
      if (['output', 'bench', 'pause', 'cont'].includes(e.Action)) continue;
      if (!e.Test) {
        if (e.Action === 'start') {
          if (packages.has(e.Package)) throw new Error('duplicate package start');
          packages.set(e.Package, 'running');
        } else if (['pass', 'skip'].includes(e.Action)) {
          if (packages.get(e.Package) !== 'running') throw new Error('package completion without unique start');
          packages.set(e.Package, e.Action);
        } else throw new Error('invalid package event');
      } else {
        const key = e.Package + '/' + e.Test;
        if (packages.get(e.Package) !== 'running') throw new Error('test outside running package');
        if (e.Action === 'run') {
          if (tests.has(key)) throw new Error('duplicate test run');
          tests.set(key, 'running');
        } else if (['pass', 'skip'].includes(e.Action)) {
          if (tests.get(key) !== 'running') throw new Error('test completion without unique run');
          tests.set(key, e.Action);
        } else throw new Error('invalid test event');
      }
    }
    if ([...packages.values(), ...tests.values()].includes('running')) throw new Error('unfinished Go execution');
    for (const [pkg, name] of profiles[profile]) {
      if (packages.get(prefix + pkg) !== 'pass' || tests.get(prefix + pkg + '/' + name) !== 'pass') throw new Error(`missing required pass: ${pkg}/${name}`);
    }
    console.log(`Coverage evidence verified: ${profile}, ${profiles[profile].length} required cases passed.`);
  } catch (error) {
    console.error(`Coverage evidence rejected: ${error.message}`);
    process.exitCode = 1;
  }
}
