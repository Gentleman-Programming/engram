import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { test } from 'node:test';
import { pathToFileURL } from 'node:url';
import { runtimeContractID, runtimeContractIdentity } from '../runtime-contract.js';
import { withPluginSandbox } from './plugin-sandbox.mjs';

// Go's current checked-in projection is the only expected inventory.
const expected = JSON.parse(await readFile(new URL('../runtime-contract.json', import.meta.url), 'utf8'));
const response = () => ({ ...structuredClone(expected), instance_id: '1'.repeat(32) });

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
