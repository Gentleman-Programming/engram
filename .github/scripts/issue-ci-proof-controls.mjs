import { randomUUID } from 'node:crypto';
import { setTimeout as sleep } from 'node:timers/promises';

const fail = (code) => new Error(`proof-control: ${code}`);
const positive = (n) => Number.isSafeInteger(n) && n > 0;
const label = (s) => typeof s === 'string' && /^[a-zA-Z0-9_.:/@-]{1,240}$/.test(s);

// Caller-supplied bindings are trusted metadata, NOT authenticated provenance.
export function createProofControl({ manifest, binding, readRelease,
  now = Date.now, nonce = randomUUID, wait = sleep, emit = () => {},
  timers = { setTimeout, clearTimeout },
} = {}) {
  if (manifest === undefined || manifest?.enabled === false) {
    return Object.freeze({ hold: async (result) => result });
  }
  // Snapshot only owned fields; never copy arbitrary metadata into receipts.
  const m = { enabled: manifest?.enabled, actorIds: Array.isArray(manifest?.actorIds)
    ? [...manifest.actorIds] : [], expiresAt: manifest?.expiresAt,
  maxReads: manifest?.maxReads, intervalMs: manifest?.intervalMs };
  const b = { case: binding?.case, runId: binding?.runId, attempt: binding?.attempt,
    phase: binding?.phase, head: binding?.head, source: binding?.source };
  let used = false;
  return Object.freeze({ hold: async (result) => {
    if (used) throw fail('used');
    used = true; // Reserve synchronously, before emitter, transport or any await.
    let started;
    try { started = now(); } catch { throw fail('configuration'); }
    if (m.enabled !== true || !m.actorIds.length || !m.actorIds.every(positive)
      || !Number.isSafeInteger(started) || !Number.isSafeInteger(m.expiresAt)
      || m.expiresAt <= started || m.expiresAt - started > 300_000
      || !positive(m.maxReads) || m.maxReads > 10
      || !positive(m.intervalMs) || m.intervalMs > 1000
      || !label(b.case) || !label(b.phase) || !label(b.source)
      || !positive(b.runId) || !positive(b.attempt)
      || typeof b.head !== 'string' || !/^[a-f0-9]{40}$/.test(b.head)
      || ![readRelease, now, nonce, wait, emit, timers?.setTimeout, timers?.clearTimeout]
        .every((fn) => typeof fn === 'function')) throw fail('configuration');
    let checkpoint;
    try { checkpoint = nonce(); } catch { throw fail('configuration'); }
    if (typeof checkpoint !== 'string' || !/^[a-zA-Z0-9_-]{16,128}$/.test(checkpoint)) {
      throw fail('configuration');
    }
    const request = Object.freeze({ ...b, checkpoint, expiresAt: m.expiresAt });
    const record = (event, actorId) => Object.freeze({ event, ...request,
      ...(actorId === undefined ? {} : { actorId }) });
    let timer;
    let lastTime = started;
    const checkTime = () => {
      let current;
      try { current = now(); } catch { throw fail('clock'); }
      if (!Number.isSafeInteger(current) || current < lastTime) throw fail('clock');
      lastTime = current;
      if (current >= m.expiresAt) throw fail('expired');
    };
    // A hung transport cannot release later: the outer deadline wins permanently.
    const deadline = new Promise((_, reject) => {
      timer = timers.setTimeout(() => reject(fail('expired')), m.expiresAt - started);
    });
    let ended = false;
    const run = async () => {
      try { emit(record('checkpoint')); } catch { throw fail('emitter'); }
      for (let i = 0; i < m.maxReads; i++) {
        if (ended) throw fail('expired');
        checkTime();
        let witness;
        try { witness = await readRelease(request); } catch { throw fail('transport'); }
        if (ended) throw fail('expired');
        checkTime();
        let matches, actorId;
        try {
          actorId = witness?.actorId;
          matches = witness !== null && typeof witness === 'object'
            && positive(actorId) && m.actorIds.includes(actorId)
            && Object.keys(request).every((key) => witness[key] === request[key]);
        } catch { throw fail('witness'); }
        if (matches) {
          try { emit(record('released', actorId)); } catch { throw fail('emitter'); }
          return result;
        }
        if (i + 1 < m.maxReads) {
          try { await wait(Math.min(m.intervalMs, m.expiresAt - lastTime)); }
          catch { throw fail('wait'); }
        }
      }
      throw fail('exhausted');
    };
    try { return await Promise.race([run(), deadline]); }
    finally { ended = true; timers.clearTimeout(timer); }
  } });
}
