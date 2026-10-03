import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createServer } from 'node:http';
import { fork } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { performance } from 'node:perf_hooks';

const bounded = async (promise, label, ms = 4000) => {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${label} deadline`)), ms); })]); }
  finally { clearTimeout(timer); }
};
const deferred = () => { let resolve; const promise = new Promise((r) => { resolve = r; }); return { promise, resolve }; };
async function stopChild(child, exit) {
  if (!child || !child.pid || child.exitCode !== null || child.signalCode !== null) return;
  assert.equal(child.kill('SIGKILL'), true, 'cleanup kill failed');
  await bounded(exit, 'cleanup child exit', 1000);
}

// Actual adapter, in-memory dependency stubs, synthetic hook host and replicated
// termination policy. NOT installed runner/Pi RPC. Fake commit occurs on release.
async function scenario(interrupted, fault) {
  const events = [];
  const requests = [];
  const record = (name, extra = {}) => events.push({ name, seq: events.length, elapsed: performance.now() - start, ...extra });
  const start = performance.now();
  const failure = deferred();
  const fail = (error) => failure.resolve(error);
  const checked = (promise, label) => bounded(Promise.race([promise, failure.promise.then((error) => { throw error; })]), label);
  const received = deferred();
  const registered = deferred();
  const identity = `synthetic-1632-${randomUUID()}`;
  let release;
  let committed = false;
  let child;
  let exit;
  const server = createServer((req, res) => {
    void (async () => {
      if (fault === 'handler') throw new Error('injected handler failure');
      let text = '';
      for await (const chunk of req) text += chunk;
      const path = new URL(req.url, 'http://127.0.0.1').pathname;
      const body = text ? JSON.parse(text) : undefined;
      requests.push({ path, method: req.method, body });
      if (path === '/project/current') {
        assert.equal(req.method, 'GET');
        assert.equal(body, undefined);
        return res.end('{"project":"synthetic-1632"}');
      }
      assert.equal(req.method, 'POST');
      if (path === '/sessions') {
        assert.equal(body.id, identity);
        assert.equal(body.project, 'synthetic-1632');
        assert.equal(body.ownership_mode, 'project_owned');
        assert.equal(body.resume, false);
        assert.equal(typeof body.directory, 'string');
        record('registration_received');
        return res.end(JSON.stringify({ id: identity, status: 'created' }));
      }
      if (path === '/observations') {
        assert.deepEqual(body, { session_id: identity, title: 'disposable', content: 'synthetic fixture only', type: 'manual', project: 'synthetic-1632', scope: 'project' });
        return res.end('{"id":1}');
      }
      assert.equal(path, `/sessions/${identity}/end`, 'unexpected request');
      assert.deepEqual(body, { summary: '' });
      record('end_received');
      res.on('close', () => record('end_connection_closed'));
      release = () => { committed = true; record('fake_commit'); res.end('{}'); };
      received.resolve();
    })().catch((error) => { fail(error); res.destroy(); });
  });
  server.on('error', fail);
  try {
    await checked(new Promise((resolve) => {
      // Deterministic listen-error injection uses the same error propagation path.
      if (fault === 'listen') server.emit('error', new Error('injected listen failure'));
      else server.listen(0, '127.0.0.1', resolve);
    }), 'listen');
    child = fork(fileURLToPath(new URL('./support/shutdown-delivery-child.mjs', import.meta.url)), [`http://127.0.0.1:${server.address().port}`, identity], {
      env: { SystemRoot: process.env.SystemRoot, PATH: process.env.PATH },
      stdio: ['ignore', 'ignore', 'pipe', 'ipc'],
      ...(fault === 'spawn' ? { execPath: `${process.execPath}.synthetic-missing` } : {}),
    });
    let stderr = '';
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('error', fail);
    exit = new Promise((resolve) => child.once('exit', (code, signal) => { record('exit', { code, signal }); resolve({ code, signal }); }));
    child.on('message', (event) => { record(event.name, { childSeq: event.seq, childAt: event.at }); if (event.name === 'registered') registered.resolve(); });
    exit.then(() => { if (!events.some((e) => e.name === 'registered')) fail(new Error(`early child exit: ${stderr}`)); });
    await checked(registered.promise, 'registration');
    record('synthetic_shutdown_command');
    child.send('shutdown', (error) => { if (error) fail(error); });
    await checked(received.promise, 'end receipt');
    assert.equal(committed, false);
    if (interrupted) {
      record('replicated_SIGTERM');
      assert.equal(child.kill('SIGTERM'), true);
      await new Promise((resolve) => setTimeout(resolve, 250));
      record('replicated_SIGKILL_250ms');
      // Kill return is deliberately not treated as delivery or exit observation.
      child.kill('SIGKILL');
    } else release();
    const outcome = await checked(exit, 'exit');
    assert.equal(committed, !interrupted);
    assert.equal(events.some((e) => e.name === 'hook_resolved'), !interrupted);
    assert.deepEqual(outcome, interrupted ? { code: null, signal: process.platform === 'win32' ? 'SIGTERM' : 'SIGKILL' } : { code: 0, signal: null });
    const counts = Object.fromEntries(['/project/current', '/sessions', '/observations', `/sessions/${identity}/end`].map((path) => [path, requests.filter((r) => r.path === path).length]));
    assert.deepEqual(Object.values(counts), [1, 1, 1, 1]);
    assert.equal(requests.length, 4, 'no unexpected requests');
    const index = (name) => events.findIndex((e) => e.name === name);
    assert.ok(index('registered') < index('synthetic_shutdown_command'));
    assert.ok(index('registration_received') < index('end_received'));
    const hook = events.find((e) => e.name === 'hook_enter');
    assert.ok(hook && hook.childSeq > events.find((e) => e.name === 'registered').childSeq);
    if (interrupted) {
      assert.ok(index('end_received') < index('replicated_SIGTERM'));
      assert.ok(index('replicated_SIGTERM') < index('exit'));
      assert.ok(index('replicated_SIGTERM') < index('replicated_SIGKILL_250ms'));
      // Timer precision and exit-versus-escalation receipt order are diagnostics
      // in events, not correctness requirements: the parent may be delayed.
      if (process.platform === 'win32') assert.equal(index('sigterm_hook'), -1);
      else assert.ok(index('sigterm_hook') >= 0);
    } else {
      assert.ok(index('end_received') < index('fake_commit'));
      assert.ok(index('fake_commit') < index('hook_resolved'));
      assert.ok(index('hook_resolved') < index('exit'));
    }
    return { platform: process.platform, counts, events, limitation: 'Windows SIGTERM is forceful; synthetic POSIX hooks are not Pi RPC. Parent monotonic receipt order and child-local sequence, not cross-process wall time, establish ordering.' };
  } finally {
    try { await stopChild(child, exit); }
    finally {
      server.closeAllConnections();
      if (server.listening) await bounded(new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve())), 'server close', 1000);
    }
  }
}
for (const interrupted of [false, true]) test(`awaited end ${interrupted ? 'interrupted' : 'orderly control'}`, { timeout: 10000 }, async (t) => t.diagnostic(JSON.stringify(await scenario(interrupted))));
for (const fault of ['listen', 'handler', 'spawn']) test(`bounded ${fault} failure cleanup`, { timeout: 10000 }, async () => { await assert.rejects(scenario(false, fault), fault === 'spawn' ? /ENOENT/ : new RegExp(`injected ${fault} failure`)); });
test('cleanup rejects failed kill and bounds missing exit', async () => {
  const child = { pid: 1, exitCode: null, signalCode: null, kill: () => false };
  await assert.rejects(stopChild(child, Promise.resolve()), /cleanup kill failed/);
  child.kill = () => true;
  await assert.rejects(stopChild(child, new Promise(() => {})), /cleanup child exit deadline/);
});
