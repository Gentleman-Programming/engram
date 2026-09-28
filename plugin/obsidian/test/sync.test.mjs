import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
const source = readFileSync(new URL('../src/sync.ts', import.meta.url), 'utf8');
const js = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
const syncNow = new Function('require', 'exports', `${js}\nreturn exports.syncNow;`)((name) => {
  if (name === 'obsidian') return { Notice: class {} };
  throw new Error(`Unexpected import ${name}`);
}, {});

function fixture(payload, projectFilter = 'alpha') {
  const files = new Map();
  const writes = [];
  const vault = {
    adapter: { exists: async p => files.has(p), read: async p => files.get(p) },
    getFileByPath: p => files.has(p) ? { path: p } : null,
    createFolder: async p => { files.set(p, null); },
    create: async (p, text) => { writes.push(p); files.set(p, text); },
    modify: async (file, text) => { writes.push(file.path); files.set(file.path, text); },
    delete: async () => { throw new Error('Unexpected deletion'); },
  };
  const plugin = { app: { vault }, settings: { engramUrl: 'http://localhost:7437', projectFilter, vaultSubfolder: 'brain' } };
  let response = payload;
  const urls = [];
  const oldFetch = globalThis.fetch;
  globalThis.fetch = async url => { urls.push(String(url)); return { ok: true, json: async () => response }; };
  return { files, writes, plugin, urls, setResponse: value => { response = value; }, restore: () => { globalThis.fetch = oldFetch; } };
}
const observation = (overrides = {}) => ({ id: 42, sync_id: 'obs-42', title: 'A title', content: 'Body', project: 'alpha', type: 'decision', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z', ...overrides });
const payload = observations => ({ version: '1', exported_at: '2026-01-02T00:00:00Z', sessions: [], observations, prompts: [], relations: [] });

test('writes observation, skips identical snapshot and updates stable note without since', async () => {
  const f = fixture(payload([observation()]));
  try {
    assert.equal((await syncNow(f.plugin)).created, 1);
    const note = [...f.files.keys()].find(p => p.endsWith('.md'));
    assert.match(f.files.get(note), /Body/);
    assert.equal((await syncNow(f.plugin)).skipped, 1);
    f.setResponse(payload([observation({ content: 'Updated', title: 'Renamed' })]));
    assert.equal((await syncNow(f.plugin)).updated, 1);
    assert.match(f.files.get(note), /Updated/);
    assert.equal([...f.files.keys()].filter(p => p.endsWith('.md')).length, 1);
    assert.equal(f.urls.every(url => !url.includes('since=') && url.includes('project=alpha')), true);
    f.setResponse(payload([]));
    assert.equal((await syncNow(f.plugin)).deleted, 0);
    assert.ok(f.files.has(note));
  } finally { f.restore(); }
});

test('imported sync IDs with separators retain numeric-id note paths', async () => {
  for (const sync_id of ['imported/one', 'imported\\two']) {
    const f = fixture(payload([observation({ sync_id })]));
    try {
      assert.equal((await syncNow(f.plugin)).created, 1);
      assert.deepEqual([...f.files.keys()].filter(p => p.endsWith('.md')), ['brain/observations/observation-42.md']);
      assert.deepEqual(f.writes.filter(p => p.endsWith('.md')), ['brain/observations/observation-42.md']);
    } finally { f.restore(); }
  }
});

test('rejects incompatible snapshots before any vault writes', async () => {
  const f = fixture({ notes: [], count: 0 });
  try {
    for (const bad of [null, { version: '1', exported_at: '2026-01-02T00:00:00Z', sessions: [], prompts: [] }, { ...payload([]), observations: {} }, payload([observation(), observation({ id: 'bad' })]), payload([observation({ project: 'other' })]), payload([observation({ sync_id: 'bad\u0001id' })])]) {
      f.setResponse(bad);
      await assert.rejects(syncNow(f.plugin), /invalid|incompatible/i);
      assert.deepEqual(f.writes, []);
      assert.equal(f.files.size, 0);
    }
    f.setResponse(payload([]));
    assert.equal((await syncNow(f.plugin)).total, 0);
  } finally { f.restore(); }
});

test('accepts Go nil collections but rejects missing observations before writes', async () => {
  const f = fixture({ version: '1', exported_at: '2026-01-02T00:00:00Z', sessions: null, observations: null, prompts: null, relations: null });
  try {
    assert.equal((await syncNow(f.plugin)).total, 0);
    const priorWrites = f.writes.length;
    f.setResponse({ version: '1', exported_at: '2026-01-02T00:00:00Z', sessions: null, prompts: null });
    await assert.rejects(syncNow(f.plugin), /incompatible/i);
    assert.equal(f.writes.length, priorWrites);
  } finally { f.restore(); }
});

test('accepts legacy null project only through selected session ownership', async () => {
  const f = fixture({ ...payload([observation({ project: null, session_id: 'owned' })]), sessions: [{ id: 'owned', project: 'alpha', directory: '', started_at: '2026-01-01T00:00:00Z' }] });
  try {
    assert.equal((await syncNow(f.plugin)).created, 1);
    const priorWrites = f.writes.length;
    f.setResponse({ ...payload([observation({ project: '', session_id: 'foreign' })]), sessions: [{ id: 'foreign', project: 'other', directory: '', started_at: '2026-01-01T00:00:00Z' }] });
    await assert.rejects(syncNow(f.plugin), /incompatible/i);
    assert.equal(f.writes.length, priorWrites);
  } finally { f.restore(); }
});

test('empty filter explicitly requests all projects', async () => {
  const f = fixture(payload([observation({ project: 'other' })]), '');
  try {
    assert.equal((await syncNow(f.plugin)).created, 1);
    assert.equal(new URL(f.urls[0]).searchParams.get('all_projects'), 'true');
    assert.equal(new URL(f.urls[0]).searchParams.has('project'), false);
  } finally { f.restore(); }
});

test('untrusted titles and projects cannot control note paths', async () => {
  const f = fixture(payload([observation({ title: '../../escape', project: '../secret', sync_id: 'safe' })]), '');
  try {
    await syncNow(f.plugin);
    const note = [...f.files.keys()].find(p => p.endsWith('.md'));
    assert.match(note, /^brain\/observations\/observation-42\.md$/);
  } finally { f.restore(); }
});
