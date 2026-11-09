import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {readFileSync} from 'node:fs';
import test from 'node:test';

const fixture = JSON.parse(readFileSync(new URL('../../internal/mcp/testdata/agent-contract-canonical-v1.json', import.meta.url), 'utf8'));
const digest = text => createHash('sha256').update(text, 'utf8').digest('hex');

// Test-only reference for this artifact's valid Unicode and canonical decimal
// safe-integer tokens. JSON.parse cannot preserve arbitrary number lexemes;
// this is not a general JSON canonicalizer or a Pi runtime verifier.
function metadataJSON(value) {
  if (Array.isArray(value)) return '[' + value.map(metadataJSON).join(',') + ']';
  if (value !== null && typeof value === 'object') {
    return '{' + Object.keys(value).sort((a, b) => Buffer.compare(Buffer.from(a, 'utf8'), Buffer.from(b, 'utf8'))).map(key => metadataJSON(key) + ':' + metadataJSON(value[key])).join(',') + '}';
  }
  if (typeof value === 'number') {
    assert.ok(Number.isSafeInteger(value) && !Object.is(value, -0), 'reference supports only canonical safe integers; use lossless number lexemes');
  }
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, char => '\\u' + char.charCodeAt(0).toString(16).padStart(4, '0'));
}

for (const vector of fixture.vectors) {
  test(`hash exact UTF-8 canonical bytes: ${vector.name}`, () => {
    assert.equal(digest(vector.canonical_utf8), vector.sha256);
  });
}

test('reconstruct Go string/key/array encoding independently', () => {
  const vector = fixture.vectors.find(vector => vector.name === 'strings-keys-arrays');
  const document = JSON.parse(vector.input_json);
  delete document.digest;
  assert.equal(metadataJSON(document), vector.canonical_utf8);
  assert.equal(digest(metadataJSON(document)), vector.sha256);
});

test('current checked-in contract digest is reproducible without executing Go', () => {
  const document = JSON.parse(readFileSync(new URL('../../docs/contracts/agent-contract-v1.json', import.meta.url), 'utf8'));
  const expected = document.digest;
  delete document.digest;
  assert.equal(digest(metadataJSON(document)), expected);
});

test('reference refuses numbers that JSON.parse cannot preserve exactly', () => {
  const vector = fixture.vectors.find(vector => vector.name === 'number-lexemes');
  const document = JSON.parse(vector.input_json);
  delete document.digest;
  assert.throws(() => metadataJSON(document), /safe integers/);
});
