import { test } from 'node:test';
import assert from 'node:assert/strict';
import { checkPluginVersion } from './claude-plugin-version.mjs';

const plugin = 'plugin/claude-code/.claude-plugin/plugin.json';
const market = '.claude-plugin/marketplace.json';
const data = (version) => ({ [plugin]: JSON.stringify({ version }), [market]: JSON.stringify({ plugins: [{ name: 'engram', version }] }) });
const run = (changes, base = data('0.1.3'), head = data('0.1.4')) => checkPluginVersion({ changes, base, head });

test('rejects plugin changes without both bumps', () => {
  assert.throws(() => run(['M\tplugin/claude-code/hooks/hook.sh'], data('0.1.3'), data('0.1.3')), /advance/);
});

test('accepts synchronized semantic increase and unrelated changes', () => {
  assert.doesNotThrow(() => run(['M\tplugin/claude-code/hooks/hook.sh']));
  assert.doesNotThrow(() => run(['M\tREADME.md'], {}, {}));
});

test('detects deleted and renamed source or destination', () => {
  for (const change of ['D\tplugin/claude-code/foo', 'R100\tplugin/claude-code/foo\tother/foo', 'R90\tother/foo\tplugin/claude-code/foo']) {
    assert.throws(() => run([change], data('0.1.3'), data('0.1.3')), /advance/);
  }
});

test('rejects mismatches, downgrades, invalid and absent metadata', () => {
  for (const head of [data('0.1.3'), data('0.1.2'), data('0.1.4-beta'), { ...data('0.1.4'), [market]: '{' }, { ...data('0.1.4'), [plugin]: undefined }, { ...data('0.1.4'), [market]: JSON.stringify({ plugins: [] }) }]) {
    assert.throws(() => run(['M\tplugin/claude-code/foo'], data('0.1.3'), head));
  }
  assert.throws(() => run(['M\tplugin/claude-code/foo'], data('bad')));
  assert.throws(() => run(['M\tplugin/claude-code/foo'], data('0.1.3'), { ...data('0.1.4'), [market]: JSON.stringify({ plugins: [{ name: 'engram', version: '0.1.5' }] }) }));
});

test('rejects malformed change records', () => {
  assert.throws(() => run(['R100\tplugin/claude-code/foo']));
  assert.throws(() => run(['M\tplugin/claude-code/foo', 'broken']));
});
