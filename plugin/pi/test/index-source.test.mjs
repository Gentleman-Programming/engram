import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const source = readFileSync(new URL("../index.ts", import.meta.url), "utf8");

function extractFunctionBody(name) {
  const signatureIndex = source.indexOf(`async function ${name}`);
  assert.notEqual(signatureIndex, -1, `${name} signature not found`);
  const bodyStart = source.indexOf("{\n  let res", signatureIndex);
  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index];
    if (char === "{") depth += 1;
    if (char === "}") depth -= 1;
    if (depth === 0) return source.slice(bodyStart + 1, index);
  }
  throw new Error(`${name} body not found`);
}

function buildEngramFetchForTest() {
  const body = extractFunctionBody("engramFetch")
    .replace("let res: Response | undefined;", "let res;")
    .replace("let data: unknown = null;", "let data = null;")
    .replace("return data as TResponse;", "return data;");
  const factory = new Function("fetch", "wait", "redactUrlPath", "redactValue", "ENGRAM_URL", `
    class EngramHttpError extends Error {
      constructor(message, status, data) {
        super(message);
        this.name = "EngramHttpError";
        this.status = status;
        this.data = data;
      }
    }
    return async function engramFetch(path, opts = {}) {
      ${body}
    };
  `);
  return factory(
    globalThis.fetch,
    () => Promise.resolve(),
    (value) => value,
    (value) => value,
    "http://127.0.0.1:7437",
  );
}

test("mem_session_summary accepts explicit project fallback", () => {
  assert.match(source, /mem_session_summary: Type\.Object\(\{[\s\S]*project: optionalString\("Optional project to use when automatic detection is unavailable"\)/);
  assert.match(source, /case "mem_session_summary":[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*ensureSession\(activeSessionId, activeProject\)[\s\S]*project: activeProject/);
});

test("project detection 404 falls back to local config or diagnostic", () => {
  assert.match(source, /function detectLocalConfigProject\(cwd: string\)/);
  assert.match(source, /project_name/);
  assert.match(source, /error\.status === 404[\s\S]*detectLocalConfigProject\(cwd\) \|\| projectCurrentUnsupportedError\(cwd\)/);
  assert.match(source, /does not support \/project\/current/);
});

test("ambiguous_project error maps to actionable status label, not generic 'error'", () => {
  // The status bar must NOT show the generic 'error' label for ambiguous project conditions.
  // Instead it should show an actionable label such as 'ambiguous project'.
  assert.match(source, /function errorStatusLabel\(/);
  // Verify the function maps ambiguous project messages to the actionable label
  assert.match(source, /ambiguous project/);
  // Verify executeMemoryTool uses errorStatusLabel instead of the bare 'error' string
  assert.match(source, /errorStatusLabel\(message\)/);
  // The bare '· error' hardcoded string should no longer be present in the catch block
  assert.doesNotMatch(source, /setStatus\?\.\("engram",\s*`🧠 \$\{project\} · error`\)/);
});

test("memory protocol declares gentle-engram as the Pi-native provider", () => {
  assert.match(source, /These instructions are injected by gentle-engram, the Pi-native memory provider/);
  assert.match(source, /Use the memory tools named in this section as the authoritative Pi memory contract/);
  assert.match(source, /Do not infer alternative Engram tool names from other integrations/);
});

test("native tool fetches retry transient HTTP startup failures", async () => {
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls < 3) throw new Error("connection refused");
    return {
      ok: true,
      async json() {
        return { status: "ok" };
      },
    };
  };
  try {
    const engramFetch = buildEngramFetchForTest();
    assert.deepEqual(await engramFetch("/health"), { status: "ok" });
    assert.equal(calls, 3);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("native tool fetch preserves HTTP error status", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: false,
    status: 503,
    async json() {
      return { error: "server warming up" };
    },
  });
  try {
    const engramFetch = buildEngramFetchForTest();
    await assert.rejects(
      () => engramFetch("/search"),
      (error) => error.name === "EngramHttpError" && error.status === 503 && error.message === "server warming up",
    );
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("native tool unavailable error names the Pi-native HTTP path", () => {
  assert.match(source, /gentle-engram could not reach the Engram HTTP server/);
  assert.match(source, /Pi-native mem_\* tools are registered/);
  assert.match(source, /Run mem_doctor or restart Engram/);
});

test("mem_review is registered as a Pi-native executable memory tool", () => {
  assert.match(source, /const ENGRAM_TOOLS = \[[\s\S]*"mem_review"/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*action: Type\.String\(\{ description: "Action: list \| mark_reviewed" \}\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*observation_id: optionalNumber\("Observation id for action=mark_reviewed"\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*id: optionalNumber\("Alias for observation_id"\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "list"[\s\S]*engramFetch\(`\/review\$\{queryString\(\{ project: params\.project, limit: params\.limit \}\)\}`\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "mark_reviewed"[\s\S]*engramFetch\("\/review\/mark_reviewed"/);
  assert.match(source, /case "mem_review":[\s\S]*body: \{ observation_id: params\.observation_id \|\| params\.id \}/);
  assert.match(source, /for \(const toolName of ENGRAM_TOOLS\)[\s\S]*executeMemoryTool\(toolName/);
});
