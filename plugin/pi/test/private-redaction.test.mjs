import test from "node:test";
import assert from "node:assert/strict";
import { redactPrivateTags, redactUrlPath, redactValue } from "../private-redaction.js";

test("redactPrivateTags redacts multiline private blocks", () => {
  const input = "keep <private>secret\nline two</private> visible";
  assert.equal(redactPrivateTags(input), "keep [REDACTED] visible");
});

test("redactUrlPath redacts private query values", () => {
  const path = "/context?project=safe&note=%3Cprivate%3Esecret%3C%2Fprivate%3E";
  assert.equal(redactUrlPath(path), "/context?project=safe&note=%5BREDACTED%5D");
});

test("redactUrlPath preserves duplicate query keys", () => {
  const path = "/context?tag=safe&tag=%3Cprivate%3Esecret%3C%2Fprivate%3E#section";
  assert.equal(redactUrlPath(path), "/context?tag=safe&tag=%5BREDACTED%5D#section");
});

test("redactValue recursively redacts strings in objects and arrays", () => {
  const input = {
    title: "safe",
    content: "before <private>token</private> after",
    nested: {
      values: ["ok", "<private>array secret</private>", 42, false, null],
    },
  };

  assert.deepEqual(redactValue(input), {
    title: "safe",
    content: "before [REDACTED] after",
    nested: {
      values: ["ok", "[REDACTED]", 42, false, null],
    },
  });
});
