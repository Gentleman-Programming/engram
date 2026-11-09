import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { test } from 'node:test';
import { pathToFileURL } from 'node:url';
import { runtimeContractID, runtimeContractIdentity } from '../runtime-contract.js';
import { importPluginFromSandbox, withPluginSandbox } from './plugin-sandbox.mjs';
import { fixtureContract } from './support/runtime-contract-fixture.mjs';

// Go's current checked-in projection is the only expected inventory.
const expected = JSON.parse(await readFile(new URL('../runtime-contract.json', import.meta.url), 'utf8'));
const response = () => ({ ...structuredClone(expected), instance_id: '1'.repeat(32) });

async function withConsumer(contract, run, endpoint = 'http://127.0.0.1:17437') {
  const previousFetch = globalThis.fetch;
  const keys = ['ENGRAM_URL', 'ENGRAM_HTTP_TOKEN', 'ENGRAM_SERVER_MODE'];
  const saved = keys.map(key => process.env[key]);
  process.env.ENGRAM_URL = endpoint;
  process.env.ENGRAM_HTTP_TOKEN = 'private-bearer';
  delete process.env.ENGRAM_SERVER_MODE;
  const calls = [], hooks = new Map(), tools = new Map();
  let current = contract;
  globalThis.fetch = async (url, init = {}) => {
    const path = new URL(url).pathname.replace(/^\/\//, '/');
    calls.push({ path, metadata: String(url).endsWith('/runtime-contract'), health: String(url).endsWith('/health'), method: init.method ?? 'GET', headers: new Headers(init.headers) });
    if (String(url).endsWith('/runtime-contract')) return typeof current === 'function' ? current() : Response.json(current);
    if (String(url).includes('/project/current')) return Response.json({ project: 'runtime-tests' });
    return Response.json({ id: 1, context: '', topic_key: 'architecture/test' });
  };
  try {
    await withPluginSandbox('engram-runtime-consumer-', async ({ sandbox, clock }) => {
      const register = await importPluginFromSandbox(sandbox);
      register({ registerTool: tool => tools.set(tool.name, tool), on: (event, handler) => hooks.set(event, handler) });
      const ctx = { cwd: sandbox, sessionManager: { getSessionId: () => 'runtime-host', getBranch: () => [] }, ui: { setStatus() {} } };
      const tool = signal => tools.get('mem_suggest_topic_key').execute('qualification', { title: 'Test' }, signal, undefined, ctx);
      await run({ tool, calls, hooks, ctx, clock, setContract: value => { current = value; } });
    }, { controlledDelay: 5000 });
  } finally {
    globalThis.fetch = previousFetch;
    keys.forEach((key, i) => { if (saved[i] === undefined) delete process.env[key]; else process.env[key] = saved[i]; });
  }
}

test('consumer refusal preserves unavailable transport and self-heal without private data or writes', async (t) => {
  const refused = () => {
    const cause = Object.assign(new Error('private-bearer'), { code: 'ECONNREFUSED' });
    throw new TypeError('private-password private-query', { cause });
  };
  await withConsumer(refused, async ({ tool, calls, clock }) => {
    t.mock.timers.enable({ apis: ['Date', 'setTimeout'], now: 0 });
    try {
      let settled = false;
      const pending = tool().then(result => { settled = true; return result; });
      // Let fetch rejections and timer cleanup settle between ticks, just as the
      // event loop does. Advancing Date alone would leave the retry sleeps pending.
      await new Promise(resolve => setImmediate(resolve));
      for (let tick = 0; tick < 99; tick++) {
        t.mock.timers.tick(100);
        await new Promise(resolve => setImmediate(resolve));
      }
      assert.equal(settled, false, 'startup remains pending before its 10s deadline');
      t.mock.timers.tick(100);
      const result = await pending;
      assert.equal(Date.now(), 10000, 'expiry uses one shared startup budget');
      assert.equal(result.isError, true);
      assert.equal(result.content[0].text, 'gentle-engram could not reach the Engram HTTP server. Run mem_doctor or restart Engram.');
      assert.doesNotMatch(JSON.stringify(result), /private-password|private-query|private-bearer/);
      assert.ok(calls.length > 1, 'initial refusal polls only the pinned contract endpoint');
      const startupCalls = calls.length;
      t.mock.timers.tick(1000);
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(calls.length, startupCalls, 'no startup probes continue after expiry');
      assert.ok(calls.every(call => call.metadata && call.method === 'GET'));
      assert.equal(clock.pending(), 1, 'typed unavailability must schedule the existing self-heal probe');
      await clock.advance();
      assert.equal(calls.filter(call => call.health).length, 1, 'self-heal must perform exactly one health probe');
      assert.equal(calls.length, startupCalls + 1, 'only qualification and the existing health probe may be dispatched');
      assert.ok(calls.every(call => call.method === 'GET'), 'qualification failure must not send lifecycle or memory writes');
    } finally {
      t.mock.timers.reset();
    }
  }, 'http://user:private-password@127.0.0.1:17437/?token=private-query');
});

for (const [name, contract] of [
  ['missing endpoint', () => new Response('', { status: 404 })],
  ['unauthorized endpoint', () => new Response('', { status: 401 })],
  ['forbidden endpoint', () => new Response('', { status: 403 })],
  ['unhealthy HTTP endpoint', () => new Response('', { status: 503 })],
  ['malformed JSON', () => new Response('{')],
  ['private transport error', () => { throw new Error('private-password private-query private-bearer'); }],
  ['unknown contract', { ...fixtureContract(), contract_id: 'unknown' }],
  ['missing identity', expected],
  ['older revision', { ...fixtureContract(), revision: 0 }],
  ['changed inventory with claimed digest', { ...fixtureContract(), operations: {} }],
  ['oversized response', () => new Response(' '.repeat(65537))],
]) {
  test(`consumer blocks ${name} before lifecycle and tool effects without leaking credentials`, async () => {
    await withConsumer(contract, async ({ tool, calls, hooks, ctx, clock }) => {
      await hooks.get('session_start')({}, ctx);
      const result = await tool();
      assert.equal(result.isError, true);
      assert.match(result.content[0].text, /runtime contract/i);
      assert.match(result.content[0].text, /upgrade|restart/i);
      assert.doesNotMatch(JSON.stringify(result), /private-password|private-query|private-bearer/);
      assert.ok(calls.length > 0);
      assert.equal(calls.length, 1, 'received HTTP and invalid contracts never enter startup polling');
      assert.ok(calls.every(call => call.method === 'GET' && call.metadata), 'only metadata may be requested');
      assert.equal(clock.pending(), 0, 'contract failures must not schedule transport self-heal');
    }, 'http://user:private-password@127.0.0.1:17437/?token=private-query');
  });
}

test('consumer requalifies exact metadata using effective shared credentials and pins instance', async () => {
  await withConsumer(fixtureContract(), async ({ tool, calls, setContract }) => {
    assert.notEqual((await tool()).isError, true);
    assert.ok(calls.some(call => call.path === '/topic-keys/suggest' && call.method === 'POST'));
    const before = calls.length;
    setContract({ ...fixtureContract(), instance_id: 'foreign-instance' });
    const result = await tool();
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /identity changed/i);
    assert.ok(calls.length > before);
    assert.equal(calls.slice(before).some(call => call.method === 'POST'), false);
    for (const call of calls) assert.equal(call.headers.get('authorization'), 'Bearer private-bearer');
  });
});

test('consumer cancellation retires only its waiter while another caller shares the probe', async () => {
  let release, started;
  const gate = new Promise(resolve => { release = resolve; });
  const entered = new Promise(resolve => { started = resolve; });
  let probes = 0;
  await withConsumer(async () => { probes++; started(); await gate; return Response.json(fixtureContract()); }, async ({ tool }) => {
    const controller = new AbortController();
    const canceled = tool(controller.signal);
    await entered;
    const survivor = tool();
    controller.abort(new DOMException('caller stopped', 'AbortError'));
    await assert.rejects(canceled, /caller stopped/);
    assert.equal(probes, 1, 'concurrent initialization shares one metadata request');
    release();
    assert.notEqual((await survivor).isError, true);
  });
});

test('cancellation during pinned refusal polling preserves a surviving startup caller', async () => {
  let refused;
  const refusal = new Promise(resolve => { refused = resolve; });
  let available = false;
  await withConsumer(() => {
    if (!available) { refused(); throw Object.assign(new Error('connection refused'), { code: 'ECONNREFUSED' }); }
    return Response.json(fixtureContract());
  }, async ({ tool, calls }) => {
    const controller = new AbortController();
    const canceled = tool(controller.signal);
    await refusal;
    const survivor = tool();
    controller.abort(new DOMException('caller stopped during startup', 'AbortError'));
    await assert.rejects(canceled, /caller stopped during startup/);
    available = true;
    assert.notEqual((await survivor).isError, true);
    assert.equal(calls.filter(call => call.path === '/topic-keys/suggest' && call.method === 'POST').length, 1);
    assert.ok(calls.filter(call => call.metadata).length > 1);
  });
});

test('a late stalled contract probe is capped by the remaining pinned startup budget', async (t) => {
  let now = Date.now();
  let probes = 0;
  let canceled = false;
  await withConsumer(() => {
    probes++;
    if (probes === 1) {
      now += 9800;
      throw Object.assign(new Error('connection refused'), { code: 'ECONNREFUSED' });
    }
    return new Response(new ReadableStream({
      start(controller) { controller.enqueue(new TextEncoder().encode('{')); },
      cancel() { canceled = true; },
    }));
  }, async ({ tool, calls }) => {
    t.mock.method(Date, 'now', () => now);
    const started = performance.now();
    const result = await tool();
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /contract probe timed out/);
    assert.ok(performance.now() - started < 2000, 'remaining 200ms must not become a fresh 3s probe');
    assert.equal(probes, 2);
    assert.equal(canceled, true, 'the timed-out response reader is canceled');
    assert.ok(calls.every(call => call.metadata && call.method === 'GET'));
  });
});

test('a refusal after successful initialization never enters the startup polling window', async () => {
  await withConsumer(fixtureContract(), async ({ tool, calls, setContract }) => {
    assert.notEqual((await tool()).isError, true);
    const before = calls.length;
    setContract(() => { throw Object.assign(new Error('connection refused'), { code: 'ECONNREFUSED' }); });
    const result = await tool();
    assert.equal(result.isError, true);
    assert.equal(calls.length, before + 1, 'post-startup qualification remains single attempt');
    assert.ok(calls.slice(before).every(call => call.metadata && call.method === 'GET'));
  });
});

test('consumer bounds a stalled metadata body by the whole probe deadline', async () => {
  await withConsumer(() => new Response(new ReadableStream({ start(controller) { controller.enqueue(new TextEncoder().encode('{')); } })), async ({ tool, calls }) => {
    const result = await tool();
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /contract probe timed out/);
    assert.ok(calls.every(call => call.metadata));
  });
});

test('public helper accepts the complete projection and returns the dynamic identity', () => {
  assert.equal(runtimeContractID, expected.contract_id);
  for (const identity of ['1'.repeat(32), '2'.repeat(32), ' opaque identity ', 'x'.repeat(128)]) {
    const actual = { ...response(), instance_id: identity };
    const before = structuredClone(actual);
    assert.equal(runtimeContractIdentity(actual), identity);
    assert.deepEqual(actual, before, 'comparison must not mutate metadata');
  }
  const reversedKeys = Object.fromEntries(Object.entries(response()).reverse());
  assert.equal(runtimeContractIdentity(reversedKeys), '1'.repeat(32), 'object key order is not contract order');
});

for (const value of [null, undefined, [], 'contract', 1]) {
  test(`public helper rejects malformed response ${JSON.stringify(value)}`, () => {
    assert.throws(() => runtimeContractIdentity(value), /malformed runtime contract/);
  });
}
for (const identity of [undefined, null, '', ' \t ', 1, {}, 'x'.repeat(129)]) {
  test(`public helper rejects invalid instance identity ${JSON.stringify(identity)}`, () => {
    assert.throws(() => runtimeContractIdentity({ ...response(), instance_id: identity }), /server instance identity/);
  });
}

const mutations = [
  ['missing contract', x => { delete x.contract_id; }],
  ['unknown contract', x => { x.contract_id = 'unknown'; }],
  ['older revision', x => { x.revision = 0; }],
  ['unknown revision', x => { x.revision = 2; }],
  ['legacy status', x => { x.status = 'mcp-foundation'; }],
  ['changed digest', x => { x.digest = '0'.repeat(64); }],
  ['unexpected top-level field', x => { x.unexpected = true; }],
  ['missing operation with unchanged digest', x => { delete x.operations.mem_context; }],
  ['extra operation', x => { x.operations.unexpected = x.operations.mem_context; }],
  ['changed binding with unchanged digest', x => { x.operations.mem_context.bindings[0].path = '/other'; }],
  ['extra binding field', x => { x.operations.mem_context.bindings[0].unexpected = true; }],
  ['binding array replaced by object', x => { x.operations.mem_context.bindings = { ...x.operations.mem_context.bindings }; }],
  ['binding array length', x => { x.operations.mem_context.bindings.push(x.operations.mem_context.bindings[0]); }],
  ['binding array order', x => { x.operations.mem_doctor.bindings.reverse(); }],
  ['configured authentication removed', x => { delete x.operations.mem_consolidate.bindings[0].configured_auth; }],
  ['supporting route removed', x => { delete x.supporting_routes['compaction-context']; }],
  ['supporting route changed', x => { x.supporting_routes['compaction-context'].method = 'POST'; }],
];
for (const [name, mutate] of mutations) {
  test(`public helper rejects ${name}`, () => {
    const actual = response();
    mutate(actual);
    assert.throws(() => runtimeContractIdentity(actual), /does not match the generated Go projection/);
  });
}

test('disposable sandbox carries both adjacent assets and an independently loadable helper', async () => {
  await withPluginSandbox('engram-runtime-assets-', async ({ sandbox }) => {
    assert.equal(await readFile(join(sandbox, 'runtime-contract.json'), 'utf8'),
      await readFile(new URL('../runtime-contract.json', import.meta.url), 'utf8'));
    const helper = await import(pathToFileURL(join(sandbox, 'runtime-contract.js')).href);
    assert.equal(helper.runtimeContractIdentity(response()), '1'.repeat(32));
    assert.equal(helper.runtimeContractID, runtimeContractID);
  });
});
