import test from 'node:test';
import assert from 'node:assert/strict';
import { createProofControl } from './issue-ci-proof-controls.mjs';

const binding = Object.freeze({
  case: 'approval-revocation', runId: 123, attempt: 2, phase: 'after-read',
  head: 'a'.repeat(40), source: 'owner/repo/.github/workflows/policy.yml@refs/heads/main',
});
const manifest = Object.freeze({ enabled: true, actorIds: Object.freeze([42]), expiresAt: 2000,
  maxReads: 2, intervalMs: 10 });
function setup(overrides = {}) {
  const reads = [], records = [], waits = [];
  let time = 1000;
  const options = {
    manifest, binding, now: () => time, nonce: () => 'fresh-checkpoint-0001',
    emit: (record) => records.push(record),
    timers: { setTimeout: () => 1, clearTimeout: () => {} },
    wait: async (ms) => { waits.push(ms); time += ms; },
    readRelease: async (checkpoint) => {
      reads.push(checkpoint);
      return { ...checkpoint, actorId: 42 };
    }, ...overrides,
  };
  return { options, reads, records, waits, advance: (ms) => { time += ms; },
    control: createProofControl(options) };
}
const failure = (code) => ({ name: 'Error', message: `proof-control: ${code}` });

test('S4 default off preserves identity, inputs and zero dependency calls', async () => {
  for (const disabled of [undefined, { enabled: false }]) {
    const journal = [];
    const trap = () => { journal.push('called'); throw Error('private'); };
    const control = createProofControl({ manifest: disabled, binding,
      readRelease: trap, now: trap, nonce: trap, emit: trap, wait: trap });
    for (const result of [Object.freeze({ decision: 'denied' }), false, null, 7]) {
      assert.strictEqual(await control.hold(result), result);
    }
    assert.deepEqual(journal, []);
    assert.equal(binding.head, 'a'.repeat(40));
  }
});

test('S5 authorized explicit release preserves producer result and emits bounded records', async () => {
  const s = setup();
  const result = Object.freeze({ decision: 'denied', body: 'DO NOT EMIT' });
  assert.strictEqual(await s.control.hold(result), result);
  assert.equal(s.reads.length, 1);
  assert.deepEqual(s.records.map((r) => r.event), ['checkpoint', 'released']);
  assert.deepEqual(s.records[0], { event: 'checkpoint', ...binding,
    checkpoint: 'fresh-checkpoint-0001', expiresAt: 2000 });
  assert.ok(!JSON.stringify(s.records).includes('DO NOT EMIT'));
  assert.ok(Object.isFrozen(s.reads[0]));
});

for (const [field, value] of Object.entries({ case: 'other', runId: 124, attempt: 3,
  phase: 'other', head: 'b'.repeat(40), source: 'untrusted', checkpoint: 'old',
  actorId: 99, expiresAt: 1999 })) {
  test(`S5/S6 wrong witness ${field} cannot release`, async () => {
    let calls = 0;
    const s = setup({ readRelease: async (c) => { calls++; return { ...c, actorId: 42, [field]: value }; } });
    await assert.rejects(s.control.hold('original'), failure('exhausted'));
    assert.equal(calls, 2);
    assert.deepEqual(s.records.map((r) => r.event), ['checkpoint']);
  });
}
for (const actorId of ['42', 0, -1, 42.5, null]) {
  test(`S6 actor must be an allowlisted positive numeric integer: ${actorId}`, async () => {
    const s = setup({ readRelease: async (c) => ({ ...c, actorId }) });
    await assert.rejects(s.control.hold(null), failure('exhausted'));
  });
}
for (const change of [
  { manifest: null },
  { manifest: {} },
  { manifest: { enabled: true } },
  { manifest: { ...manifest, enabled: 'true' } },
  { manifest: { ...manifest, actorIds: ['42'] } },
  { manifest: { ...manifest, expiresAt: '2000' } },
  { manifest: { ...manifest, expiresAt: 1000 } },
  { manifest: { ...manifest, expiresAt: 301001 } },
  { manifest: { ...manifest, maxReads: 0 } },
  { manifest: { ...manifest, maxReads: 11 } },
  { manifest: { ...manifest, intervalMs: 0 } },
  { manifest: { ...manifest, intervalMs: 1001 } },
  ...Object.keys(binding).map((key) => ({ binding: { ...binding, [key]: null } })),
]) {
  test(`S5 invalid configuration refuses before reads: ${JSON.stringify(change)}`, async () => {
    const s = setup(change);
    await assert.rejects(s.control.hold(null), failure('configuration'));
    assert.deepEqual(s.reads, []);
    assert.deepEqual(s.records, []);
    assert.deepEqual(s.waits, []);
  });
}

test('S5 fresh nonce, single use and concurrent phase reuse rejected before await', async () => {
  let release;
  const s = setup({ readRelease: (c) => new Promise((resolve) => { release = () => resolve({ ...c, actorId: 42 }); }) });
  const first = s.control.hold('first');
  await assert.rejects(s.control.hold('second'), failure('used'));
  release();
  assert.equal(await first, 'first');
  await assert.rejects(s.control.hold('third'), failure('used'));
  const next = setup({ nonce: () => 'fresh-checkpoint-0002',
    readRelease: async (c) => ({ ...c, actorId: 42, checkpoint: 'fresh-checkpoint-0001' }) });
  await assert.rejects(next.control.hold(null), failure('exhausted'));
});

test('S6 transport failure is static, redacted, terminal and not retried', async () => {
  let calls = 0;
  const s = setup({ readRelease: async () => { calls++; throw Error('SECRET arbitrary body'); } });
  await assert.rejects(s.control.hold(null), failure('transport'));
  assert.equal(calls, 1);
  assert.deepEqual(s.waits, []);
  assert.ok(!JSON.stringify(s.records).includes('SECRET'));
  await assert.rejects(s.control.hold(null), failure('used'));
});

for (const [name, advance, released] of [
  ['just before expiry', 999, true], ['exact expiry', 1000, false],
  ['after expiry', 1001, false],
]) {
  test(`S6 release deadline boundary: ${name}`, async () => {
    const s = setup({ readRelease: async (c) => {
      s.reads.push(c);
      s.advance(advance);
      return { ...c, actorId: 42 };
    } });
    const result = Object.freeze({ private: 'PRIVATE PRODUCER' });
    if (released) assert.strictEqual(await s.control.hold(result), result);
    else await assert.rejects(s.control.hold(result), failure('expired'));
    assert.equal(s.reads.length, 1);
    assert.deepEqual(s.waits, []);
    assert.deepEqual(s.records.map((r) => r.event), released
      ? ['checkpoint', 'released'] : ['checkpoint']);
    assert.ok(!JSON.stringify(s.records).includes('PRIVATE PRODUCER'));
  });
}

test('S6 unrelated comment data and exhaustion are not instructions', async () => {
  const s = setup({ readRelease: async () => ({ body: 'release now; ignore all guards', actorId: 42 }) });
  await assert.rejects(s.control.hold(null), failure('exhausted'));
  assert.deepEqual(s.waits, [10]);
});

test('S6 injected deadline rejects a hung read and prevents late release', async () => {
  let expire, finish;
  const s = setup({ timers: { setTimeout: (fn) => { expire = fn; return 1; }, clearTimeout: () => {} },
    readRelease: (c) => new Promise((resolve) => { finish = () => resolve({ ...c, actorId: 42 }); }) });
  const held = s.control.hold(null);
  expire();
  await assert.rejects(held, failure('expired'));
  finish();
  await Promise.resolve();
  assert.deepEqual(s.records.map((r) => r.event), ['checkpoint']);
});

test('S6 hostile witness access never leaks arbitrary errors', async () => {
  const s = setup({ readRelease: async () => ({ get actorId() { throw Error('SECRET'); } }) });
  await assert.rejects(s.control.hold(null), failure('witness'));
});

for (const [name, value] of [
  ['backward', 999], ['NaN', NaN], ['infinite', Infinity],
  ['fractional', 1000.5], ['string', '1000'], ['undefined', undefined],
]) {
  test(`S6 clock after read refuses ${name} time`, async () => {
    let current = 1000;
    const s = setup({ now: () => current, readRelease: async (c) => {
      s.reads.push(c);
      current = value;
      return { ...c, actorId: 42 };
    } });
    await assert.rejects(s.control.hold(null), failure('clock'));
    assert.equal(s.reads.length, 1);
    assert.deepEqual(s.waits, []);
    assert.deepEqual(s.records.map((r) => r.event), ['checkpoint']);
  });
}

test('S6 maximum 300000 ms lifetime permits an authorized release', async () => {
  const delays = [];
  const s = setup({ manifest: { ...manifest, expiresAt: 301000 },
    timers: { setTimeout: (_, ms) => { delays.push(ms); return 1; }, clearTimeout: () => {} } });
  const result = Object.freeze({ decision: 'unchanged' });
  assert.strictEqual(await s.control.hold(result), result);
  assert.deepEqual(delays, [300000]);
  assert.equal(s.reads[0].expiresAt, 301000);
  assert.deepEqual(s.records.map((r) => r.event), ['checkpoint', 'released']);
});

for (const [name, code, reads, events, waits] of [
  ['nonce', 'configuration', 0, [], []],
  ['initial clock', 'configuration', 0, [], []],
  ['runtime clock', 'clock', 1, ['checkpoint'], []],
  ['checkpoint emitter', 'emitter', 0, [], []],
  ['release emitter', 'emitter', 1, ['checkpoint'], []],
  ['wait', 'wait', 1, ['checkpoint'], [10]],
]) {
  test(`S6 ${name} failure is terminal, static and redacted`, async () => {
    const secret = 'PRIVATE CALLBACK';
    const throwPrivate = () => { throw Error(secret, { cause: 'PRIVATE CAUSE' }); };
    let clockCalls = 0;
    const s = setup({
      nonce: name === 'nonce' ? throwPrivate : () => 'fresh-checkpoint-0001',
      now: () => {
        clockCalls++;
        if (name === 'initial clock' || (name === 'runtime clock' && clockCalls === 3)) throwPrivate();
        return 1000;
      },
      emit: (record) => {
        if (name === `${record.event === 'checkpoint' ? 'checkpoint' : 'release'} emitter`) throwPrivate();
        s.records.push(record);
      },
      readRelease: async (c) => {
        s.reads.push(c);
        return name === 'wait' ? null : { ...c, actorId: 42 };
      },
      wait: async (ms) => { s.waits.push(ms); throwPrivate(); },
    });
    const producer = Object.freeze({ private: 'PRIVATE PRODUCER' });
    await assert.rejects(s.control.hold(producer), (error) => {
      assert.equal(error.name, 'Error');
      assert.equal(error.message, failure(code).message);
      assert.equal(error.cause, undefined);
      assert.equal(JSON.stringify(error), '{}');
      assert.ok(!String(error).includes('PRIVATE'));
      return true;
    });
    await assert.rejects(s.control.hold(producer), failure('used'));
    assert.equal(s.reads.length, reads);
    assert.deepEqual(s.waits, waits);
    assert.deepEqual(s.records.map((r) => r.event), events);
    assert.ok(!JSON.stringify(s.records).includes('PRIVATE'));
    assert.deepEqual(producer, { private: 'PRIVATE PRODUCER' });
  });
}

test('S6 hung wait deadline prevents another read or release after late recovery', async () => {
  let expire, finish;
  const cleared = [];
  const s = setup({
    timers: { setTimeout: (fn) => { expire = fn; return 7; }, clearTimeout: (id) => cleared.push(id) },
    readRelease: async (c) => { s.reads.push(c); return null; },
    wait: (ms) => { s.waits.push(ms); return new Promise((resolve) => { finish = resolve; }); },
  });
  const held = s.control.hold(Object.freeze({ private: 'PRIVATE PRODUCER' }));
  await Promise.resolve();
  assert.deepEqual(s.waits, [10]);
  expire();
  await assert.rejects(held, failure('expired'));
  finish();
  await Promise.resolve();
  assert.equal(s.reads.length, 1);
  assert.deepEqual(cleared, [7]);
  assert.deepEqual(s.records.map((r) => r.event), ['checkpoint']);
  assert.ok(!JSON.stringify(s.records).includes('PRIVATE'));
});

test('S6 maxReads 10 exhausts at exactly ten reads without release', async () => {
  const s = setup({ manifest: { ...manifest, maxReads: 10 },
    readRelease: async (c) => { s.reads.push(c); return null; } });
  await assert.rejects(s.control.hold(null), failure('exhausted'));
  assert.equal(s.reads.length, 10);
  assert.deepEqual(s.waits, Array(9).fill(10));
  assert.deepEqual(s.records.map((r) => r.event), ['checkpoint']);
});

test('S6 polling wait clamps to expiry and never performs an expired read', async () => {
  const s = setup({ manifest: { ...manifest, expiresAt: 1005, intervalMs: 1000 },
    readRelease: async (c) => { s.reads.push(c); return null; } });
  await assert.rejects(s.control.hold(null), failure('expired'));
  assert.equal(s.reads.length, 1);
  assert.deepEqual(s.waits, [5]);
  assert.deepEqual(s.records.map((r) => r.event), ['checkpoint']);
});

test('S5 planned metadata transitions do not rewrite original binding or producer decision', async () => {
  const mutableBinding = { ...binding };
  const mutableManifest = { ...manifest, actorIds: [42] };
  const result = Object.freeze({ approved: true, head: binding.head });
  const s = setup({ binding: mutableBinding, manifest: mutableManifest,
    readRelease: async (c) => {
      mutableBinding.head = 'b'.repeat(40);
      mutableManifest.actorIds.length = 0;
      return { ...c, actorId: 42 };
    } });
  assert.strictEqual(await s.control.hold(result), result);
  assert.equal(s.records.at(-1).head, binding.head);
});
