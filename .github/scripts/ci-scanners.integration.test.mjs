import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { crc32 } from 'node:zlib';

// Store-only ZIP: no dependency or external archiver needed for a local Go proxy.
function moduleZip(files) {
  const local = [], central = [];
  let offset = 0;
  for (const [file, text] of Object.entries(files)) {
    const name = Buffer.from(`example.com/fake@v1.0.0/${file}`), data = Buffer.from(text);
    const header = Buffer.alloc(30), entry = Buffer.alloc(46);
    header.writeUInt32LE(0x04034b50); header.writeUInt16LE(20, 4);
    header.writeUInt32LE(crc32(data), 14);
    header.writeUInt32LE(data.length, 18); header.writeUInt32LE(data.length, 22);
    header.writeUInt16LE(name.length, 26);
    entry.writeUInt32LE(0x02014b50); entry.writeUInt16LE(20, 4); entry.writeUInt16LE(20, 6);
    header.copy(entry, 8, 6, 28);
    entry.writeUInt32LE(offset, 42);
    local.push(header, name, data); central.push(entry, name);
    offset += header.length + name.length + data.length;
  }
  const directory = Buffer.concat(central), end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50); end.writeUInt16LE(central.length / 2, 8);
  end.writeUInt16LE(central.length / 2, 10); end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...local, directory, end]);
}

// Deliberately opt-in: policy tests use mocks; this suite needs real pinned tools.
const bin = process.env.CI_SCANNER_BIN_DIR;
test('real pinned scanner CLI contracts', { skip: !bin && 'Set CI_SCANNER_BIN_DIR to run real scanners' }, async (t) => {
  assert.ok(path.isAbsolute(bin), 'CI_SCANNER_BIN_DIR must be an absolute native path');
  const executable = (name) => path.join(bin, name + (process.platform === 'win32' ? '.exe' : ''));
  for (const name of ['govulncheck', 'actionlint']) assert.ok(fs.existsSync(executable(name)), `missing ${name}`);
  const scratch = path.resolve('.repro-2019');
  fs.mkdirSync(scratch, { recursive: true });
  fs.writeFileSync(path.join(scratch, '.gitignore'), '*\n');
  const dir = fs.mkdtempSync(path.join(scratch, 'integration-'));
  const write = (file, content) => {
    const target = path.join(dir, file);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, typeof content === 'string' || Buffer.isBuffer(content) ? content : JSON.stringify(content));
    return target;
  };
  const env = { ...process.env, GOTOOLCHAIN: 'local', GOFLAGS: '-mod=readonly', GOWORK: 'off',
    GOPROXY: pathToFileURL(path.join(dir, 'proxy')).href, GOSUMDB: 'off',
    GOMODCACHE: path.join(dir, 'modcache'), GOPRIVATE: '', GONOPROXY: '', GONOSUMDB: '', CGO_ENABLED: '0' };
  const run = (command, args) => {
    const result = spawnSync(command, args, { cwd: dir, env, encoding: 'utf8', timeout: 120000 });
    assert.ifError(result.error);
    assert.equal(result.signal, null);
    assert.equal(typeof result.stdout, 'string');
    assert.equal(typeof result.stderr, 'string');
    return result;
  };
  assert.match(run('go', ['version']).stdout, /^go version go1\.26\.10 /);
  assert.match(run(executable('govulncheck'), ['-version']).stdout, /v1\.7\.0/);
  assert.match(run(executable('actionlint'), ['-version']).stdout, /1\.7\.12/);
  write('go.mod', 'module example.com/scanner-fixture\n\ngo 1.25.10\n\nrequire example.com/fake v1.0.0\n');
  write('main.go', 'package main\nimport "example.com/fake"\nfunc main() { fake.Vulnerable() }\n');
  const fakeMod = 'module example.com/fake\n\ngo 1.25.10\n';
  write('proxy/example.com/fake/@v/v1.0.0.mod', fakeMod);
  write('proxy/example.com/fake/@v/v1.0.0.info', { Version: 'v1.0.0', Time: '2026-01-01T00:00:00Z' });
  write('proxy/example.com/fake/@v/list', 'v1.0.0\n');
  write('proxy/example.com/fake/@v/v1.0.0.zip', moduleZip({
    'go.mod': fakeMod, 'fake.go': 'package fake\nfunc Vulnerable() {}\n',
  }));
  // A path replacement loses the version needed for vulnerability matching.
  const download = run('go', ['mod', 'download', 'example.com/fake']);
  assert.equal(download.status, 0, download.stdout + download.stderr);
  const sumBefore = fs.readFileSync(path.join(dir, 'go.sum'), 'utf8');
  const moduleBefore = fs.readFileSync(path.join(dir, 'go.mod'), 'utf8');
  const modified = '2026-01-01T00:00:00Z';
  const id = 'GO-2099-0001';
  // v1 local database endpoints: internal/client/{client,schema}.go at v1.7.0.
  for (const db of ['clean', 'finding', 'broken']) write(`${db}/index/db.json`, { modified });
  write('clean/index/modules.json', []);
  write('broken/index/modules.json', '{invalid');
  write('finding/index/modules.json', [{ path: 'example.com/fake', vulns: [{ id, modified, fixed: '1.0.1' }] }]);
  write(`finding/ID/${id}.json`, {
    schema_version: '1.3.1', id, modified, published: modified,
    details: 'Synthetic test record only; no real vulnerable dependency.',
    affected: [{ package: { name: 'example.com/fake', ecosystem: 'Go' },
      ranges: [{ type: 'SEMVER', events: [{ introduced: '0' }, { fixed: '1.0.1' }] }],
      ecosystem_specific: { imports: [{ path: 'example.com/fake', symbols: ['Vulnerable'] }] } }],
    database_specific: { url: `https://pkg.go.dev/vuln/${id}` },
  });
  const scan = (db, pattern = './...') => run(executable('govulncheck'), [
    '-db', pathToFileURL(path.join(dir, db)).href, '-format=text', '-scan=symbol', pattern,
  ]);
  await t.test('empty v1 database is clean', () => {
    const r = scan('clean');
    assert.equal(r.status, 0, r.stderr);
    assert.match(r.stdout, /No vulnerabilities found/);
    assert.equal(r.stderr, '');
  });
  await t.test('called synthetic vulnerable symbol returns finding status 3', () => {
    const r = scan('finding');
    assert.equal(r.status, 3, r.stdout + r.stderr);
    assert.match(r.stdout, /GO-2099-0001/);
    assert.match(r.stdout, /Vulnerable/);
    assert.equal(r.stderr, '');
  });
  for (const [label, db, pattern] of [['broken database', 'broken', './...'], ['package load failure', 'clean', './absent']]) {
    await t.test(`${label} never reports clean`, () => {
      const r = scan(db, pattern);
      assert.notEqual(r.status, 0);
      assert.notEqual(r.status, 3);
      assert.match(r.stderr, /\S/);
      assert.doesNotMatch(r.stdout, /No vulnerabilities found/);
      assert.equal(fs.readFileSync(path.join(dir, 'go.mod'), 'utf8'), moduleBefore);
      assert.equal(fs.readFileSync(path.join(dir, 'go.sum'), 'utf8'), sumBefore);
    });
  }
  const clean = write('clean.yml', 'name: Fixture\non: push\njobs:\n  check:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo safe\n');
  const malformed = write('malformed.yml', 'jobs: [\n');
  for (const [label, args, status] of [
    ['clean workflow', [clean], 0], ['malformed YAML', [malformed], 1],
    ['invocation failure', ['-not-a-real-flag'], 2],
  ]) {
    await t.test(`actionlint ${label} returns ${status}`, () => {
      const r = run(executable('actionlint'), ['-color', '-shellcheck=', '-pyflakes=', ...args]);
      assert.equal(r.status, status, r.stdout + r.stderr);
      if (status === 0) { assert.equal(r.stdout, ''); assert.equal(r.stderr, ''); }
      else assert.match(r.stdout + r.stderr, /\S/);
    });
  }
  assert.equal(fs.readFileSync(path.join(dir, 'go.mod'), 'utf8'), moduleBefore);
  // Retained intentionally for inspection; all mutable fixtures remain ignored inside scratch.
});
