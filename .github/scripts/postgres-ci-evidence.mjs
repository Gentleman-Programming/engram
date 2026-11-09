import fs from 'node:fs';

// These existing tests enter the ordinary DSN-gated helpers in each package.
const prefix = 'github.com/Gentleman-Programming/engram/v3/';
const required = new Map([
  ['internal/cloud/cloudstore', 'TestProjectSyncControlPersists'],
  ['internal/cloud/cloudserver', 'TestSessionAuthorityRegistrationPersistsOverHTTP'],
  ['cmd/engram', 'TestCloudRuntimeWiresManagedTokenAuthEndToEnd'],
].map(([pkg, name]) => [prefix + pkg, name]));
try {
  if (process.argv.length !== 3) throw new Error('Usage: node .github/scripts/postgres-ci-evidence.mjs EVENTS.json');
  const packages = new Set();
  const tests = new Set();
  for (const line of fs.readFileSync(process.argv[2], 'utf8').split('\n')) {
    if (!line.trim()) continue;
    const event = JSON.parse(line);
    if (!event || typeof event !== 'object' || typeof event.Action !== 'string') throw new Error('invalid test event');
    if (event.Action === 'fail') throw new Error('Go test failure');
    if (typeof event.Output === 'string' && /CLOUDSTORE_TEST_DSN/.test(event.Output) && /skip/i.test(event.Output)) throw new Error('database test skipped because of DSN configuration');
    if (event.Action === 'pass') {
      if (!event.Test) packages.add(event.Package);
      else if (required.get(event.Package) === event.Test) tests.add(event.Package);
    }
  }
  for (const [pkg, name] of required) {
    if (!packages.has(pkg)) throw new Error(`missing package pass: ${pkg}`);
    if (!tests.has(pkg)) throw new Error(`missing database test pass: ${pkg}/${name}`);
  }
  console.log('PostgreSQL evidence verified: 3 packages and 3 database sentinel tests passed.');
} catch (error) {
  console.error(`PostgreSQL evidence rejected: ${error.message}`);
  process.exitCode = 1;
}
