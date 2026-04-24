import assert from "node:assert/strict"
import os from "node:os"
import path from "node:path"
import { pathToFileURL } from "node:url"
import { readFile, rm, writeFile } from "node:fs/promises"

const extensionPath = process.argv[2]
if (!extensionPath) {
  throw new Error("usage: node runtime-harness.mjs <extension-path>")
}

function response(status, payload, contentType = "application/json") {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: {
      get(key) {
        if (String(key).toLowerCase() === "content-type") {
          return contentType
        }
        return null
      },
    },
    async json() {
      return payload
    },
    async text() {
      return typeof payload === "string" ? payload : JSON.stringify(payload)
    },
  }
}

let source = await readFile(extensionPath, "utf8")
assert.ok(!source.includes("Bun.spawn("), "extension must not use Bun.spawn")

source = source.replace(
  /^import type \{ ExtensionAPI \} from "@mariozechner\/pi-coding-agent"\n/m,
  "",
)

source = source.replace(
  'import { Type } from "typebox"',
  `const Type = {
  Object: (shape) => ({ kind: "object", shape }),
  String: (opts = {}) => ({ kind: "string", ...opts }),
  Number: (opts = {}) => ({ kind: "number", ...opts }),
  Optional: (schema) => ({ ...schema, optional: true }),
}`,
)

source = source.replace(
  'import { spawn } from "node:child_process"',
  "const spawn = (...args) => globalThis.__ENGRAM_TEST_SPAWN__(...args)",
)

const transformedPath = path.join(os.tmpdir(), `engram-pi-runtime-${Date.now()}.ts`)
await writeFile(transformedPath, source)

let moduleUnderTest
try {
  moduleUnderTest = await import(`${pathToFileURL(transformedPath).href}?v=${Date.now()}`)
  assert.equal(typeof moduleUnderTest.default, "function", "default export must be a function")
} finally {
  await rm(transformedPath, { force: true })
}

if (typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout !== "function") {
  AbortSignal.timeout = () => undefined
}

const spawnCalls = []
let spawnShouldThrow = false
globalThis.__ENGRAM_TEST_SPAWN__ = (command, args, options) => {
  if (spawnShouldThrow) {
    throw new Error("spawn exploded")
  }
  const record = { command, args, options, unrefCalled: false }
  spawnCalls.push(record)
  return {
    unref() {
      record.unrefCalled = true
    },
  }
}

let healthChecks = 0
let recentPayload = []
let healthAlwaysDown = false
const fetchCalls = []
const sessionEndBodies = []
const sessionSummaries = new Map()
const sessionEndCountById = new Map()
const lowSignalShutdownSummary = /^shutdown reason=.* target=.*$/

function getIncomingSummary(summary) {
  return typeof summary === "string" ? summary : ""
}

function shouldPreserveSummary(currentSummary, incomingSummary) {
  return (
    typeof currentSummary === "string" &&
    currentSummary.trim() !== "" &&
    lowSignalShutdownSummary.test(incomingSummary.trim())
  )
}

function applySessionSummary(sessionId, incomingSummary) {
  if (shouldPreserveSummary(sessionSummaries.get(sessionId), incomingSummary)) {
    return
  }

  if (incomingSummary.trim() === "") {
    sessionSummaries.delete(sessionId)
    return
  }

  sessionSummaries.set(sessionId, incomingSummary)
}

globalThis.fetch = async (rawUrl, init = {}) => {
  const url = new URL(rawUrl)
  fetchCalls.push(url.pathname + url.search)

  if (url.pathname === "/health") {
    healthChecks += 1
    if (healthAlwaysDown) {
      return response(503, { ok: false })
    }
    if (healthChecks === 1) {
      return response(503, { ok: false })
    }
    return response(200, { ok: true })
  }

  if (url.pathname === "/search") {
    return response(200, [])
  }

  if (url.pathname === "/context") {
    return response(200, { observations: [] })
  }

  if (url.pathname === "/observations/recent") {
    return response(200, recentPayload)
  }

  if (url.pathname === "/sessions" || url.pathname.includes("/sessions/")) {
    if (url.pathname.includes("/sessions/") && init.body) {
      try {
        const payload = JSON.parse(String(init.body))
        sessionEndBodies.push(payload)
        const match = url.pathname.match(/^\/sessions\/([^/]+)\/end$/)
        if (match) {
          const sessionId = decodeURIComponent(match[1])
          const incomingSummary = getIncomingSummary(payload.summary)
          applySessionSummary(sessionId, incomingSummary)

          sessionEndCountById.set(sessionId, (sessionEndCountById.get(sessionId) ?? 0) + 1)
        }
      } catch {
        sessionEndBodies.push({ parseError: true })
      }
    }
    return response(200, { ok: true })
  }

  if (url.pathname === "/prompts") {
    return response(200, { ok: true })
  }

  return response(200, { ok: true })
}

const tools = []
const commands = []
const hooks = new Map()
const pi = {
  registerTool(tool) {
    tools.push(tool)
  },
  registerCommand(name, definition) {
    commands.push({ name, definition })
  },
  on(eventName, handler) {
    hooks.set(eventName, handler)
  },
}

moduleUnderTest.default(pi)

const expectedToolNames = [
  "mem_search",
  "mem_context",
  "mem_save",
  "mem_session_summary",
  "mem_get_observation",
  "mem_save_prompt",
]

assert.equal(tools.length, expectedToolNames.length, "must register all native pi tools")
for (const name of expectedToolNames) {
  const tool = tools.find((item) => item.name === name)
  assert.ok(tool, `missing tool registration for ${name}`)
  assert.equal(typeof tool.name, "string")
  assert.equal(typeof tool.label, "string")
  assert.equal(typeof tool.description, "string")
  assert.equal(typeof tool.parameters, "object")
  assert.equal(typeof tool.execute, "function")
}

assert.equal(commands.length, 1, "must register one command")
assert.equal(commands[0].name, "engram-recovery", "command must use string-first registration")
assert.equal(typeof commands[0].definition.handler, "function")

const memSearch = tools.find((item) => item.name === "mem_search")
const runtimeContext = {
  cwd: "/tmp/worktrees/engram",
  sessionManager: { getSessionFile: () => "/tmp/worktrees/engram/.pi/sessions/runtime-session.json" },
  hasUI: true,
  ui: { notify() {} },
}

const searchResult = await memSearch.execute("call-1", { query: "runtime" }, undefined, undefined, runtimeContext)
assert.ok(Array.isArray(searchResult.content), "tool result must provide content array")
assert.equal(searchResult.content[0].type, "text", "tool result content item must be text")
assert.equal(typeof searchResult.content[0].text, "string")
assert.ok(searchResult.content[0].text.length > 0)

assert.ok(spawnCalls.length >= 1, "backend bootstrap should spawn ENGRAM_BIN when health check fails")
assert.equal(spawnCalls[0].command, "engram", "default ENGRAM_BIN should be used")
assert.deepEqual(spawnCalls[0].args, ["serve"])
assert.equal(spawnCalls[0].options?.detached, true)
assert.equal(spawnCalls[0].options?.stdio, "ignore")
assert.equal(spawnCalls[0].unrefCalled, true)

const sessionStart = hooks.get("session_start")
assert.equal(typeof sessionStart, "function", "session_start hook must be registered")

const startupCases = [
  { payload: [], expectNotify: false },
  { payload: [{ id: 1 }], expectNotify: true },
  { payload: { observations: [{ id: 2 }] }, expectNotify: true },
  { payload: { data: [{ id: 3 }] }, expectNotify: true },
  { payload: { data: { observations: [{ id: 4 }] } }, expectNotify: true },
  { payload: { unknown: true }, expectNotify: false },
]

for (const tc of startupCases) {
  const notifications = []
  recentPayload = tc.payload
  await sessionStart(
    { reason: "startup" },
    {
      ...runtimeContext,
      hasUI: true,
      ui: {
        notify(message, level) {
          notifications.push({ message, level })
        },
      },
    },
  )

  if (tc.expectNotify) {
    assert.ok(notifications.length > 0, "startup should notify when recent observations exist")
  } else {
    assert.equal(notifications.length, 0, "startup must stay quiet when no recent observations")
  }
}

const contextCallsAfterStartup = fetchCalls.filter((entry) => entry.startsWith("/context"))
assert.equal(contextCallsAfterStartup.length, 0, "session_start must be notify-only (no /context auto-injection)")

const sessionBeforeCompact = hooks.get("session_before_compact")
const sessionCompact = hooks.get("session_compact")
const sessionShutdown = hooks.get("session_shutdown")
assert.equal(typeof sessionBeforeCompact, "function", "session_before_compact hook must be registered")
assert.equal(typeof sessionCompact, "function", "session_compact hook must be registered")
assert.equal(typeof sessionShutdown, "function", "session_shutdown hook must be registered")

const compactionNotifications = []
const compactionContext = {
  ...runtimeContext,
  hasUI: true,
  ui: {
    notify(message, level) {
      compactionNotifications.push({ message, level })
    },
  },
}

const beforeCompactionCases = [
  {
    name: "array_customInstructions",
    event: { customInstructions: ["existing"] },
    expectFirst: true,
  },
  {
    name: "string_customInstructions",
    event: { customInstructions: "existing" },
    expectPrefix: true,
  },
  {
    name: "object_items_customInstructions",
    event: { customInstructions: { items: ["existing"] } },
    expectNestedFirst: true,
  },
  {
    name: "nested_compactionEntry_value",
    event: { compactionEntry: { customInstructions: { value: "existing" } } },
    expectNestedPrefix: true,
  },
  {
    name: "unsupported_payload_notifies_recovery",
    event: { unsupported: true },
    expectRecoveryNotice: true,
  },
]

for (const tc of beforeCompactionCases) {
  const beforeCount = compactionNotifications.length
  await sessionBeforeCompact(tc.event, compactionContext)

  if (tc.expectFirst) {
    assert.equal(
      tc.event.customInstructions[0],
      "FIRST ACTION REQUIRED: Call mem_session_summary with the compacted summary first, then call mem_context before continuing.",
      `session_before_compact should prepend instruction for ${tc.name}`,
    )
  }

  if (tc.expectPrefix) {
    assert.ok(
      String(tc.event.customInstructions).startsWith("FIRST ACTION REQUIRED:"),
      `session_before_compact should prefix instruction for ${tc.name}`,
    )
    assert.ok(
      String(tc.event.customInstructions).includes("call mem_context before continuing"),
      `session_before_compact should include recovery steps for ${tc.name}`,
    )
  }

  if (tc.expectNestedFirst) {
    assert.equal(
      tc.event.customInstructions.items[0],
      "FIRST ACTION REQUIRED: Call mem_session_summary with the compacted summary first, then call mem_context before continuing.",
      `session_before_compact should inject nested item for ${tc.name}`,
    )
  }

  if (tc.expectNestedPrefix) {
    assert.ok(
      String(tc.event.compactionEntry.customInstructions.value).startsWith("FIRST ACTION REQUIRED:"),
      `session_before_compact should prefix nested compactionEntry instructions for ${tc.name}`,
    )
  }

  if (tc.expectRecoveryNotice) {
    assert.ok(
      compactionNotifications.length > beforeCount,
      `session_before_compact should notify recovery when payload shape unsupported (${tc.name})`,
    )
    const last = compactionNotifications[compactionNotifications.length - 1]
    assert.ok(String(last.message).includes("FIRST ACTION REQUIRED"))
    assert.ok(String(last.message).includes("mem_context"))
    assert.equal(last.level, "info")
  }
}

const compactCases = [
  {
    name: "direct_summary",
    event: { summary: "compacted summary A" },
    expectSaved: true,
  },
  {
    name: "nested_compaction_entry",
    event: { compactionEntry: { finalSummary: "compacted summary B" } },
    expectSaved: true,
  },
  {
    name: "private_summary_redacted",
    event: { compactSummary: "keep <private>secret</private> safe" },
    expectSaved: true,
  },
  {
    name: "missing_summary_notifies_recovery",
    event: { empty: true },
    expectSaved: false,
  },
]

for (const tc of compactCases) {
  const beforeBodies = sessionEndBodies.length
  const beforeNotifications = compactionNotifications.length
  await sessionCompact(tc.event, compactionContext)

  if (tc.expectSaved) {
    assert.ok(sessionEndBodies.length > beforeBodies, `session_compact should persist summary for ${tc.name}`)
    const payload = sessionEndBodies[sessionEndBodies.length - 1]
    assert.equal(typeof payload.summary, "string")
    assert.ok(payload.summary.length > 0)
  } else {
    assert.equal(sessionEndBodies.length, beforeBodies, `session_compact should skip persistence for ${tc.name}`)
    assert.ok(
      compactionNotifications.length > beforeNotifications,
      `session_compact should notify when summary unavailable (${tc.name})`,
    )
    const recent = compactionNotifications.slice(beforeNotifications)
    assert.ok(recent.some((item) => String(item.message).includes("FIRST ACTION REQUIRED")))
    assert.ok(recent.some((item) => String(item.message).includes("mem_context")))
  }
}

const lastSaved = sessionEndBodies[sessionEndBodies.length - 1]
assert.ok(
  sessionEndBodies.some((payload) => String(payload.summary).includes("[REDACTED]")),
  "session_compact persistence should sanitize <private> blocks",
)
assert.ok(lastSaved || sessionEndBodies.length > 0, "compaction runtime harness must validate persisted summary payloads")

const summaryBeforeShutdown = sessionSummaries.get("runtime-session")
assert.equal(typeof summaryBeforeShutdown, "string", "compaction should persist a runtime-session summary")
const runtimeEndsBeforeShutdown = sessionEndCountById.get("runtime-session") ?? 0
await sessionShutdown({ reason: "exit", target: "app" }, runtimeContext)
const summaryAfterShutdown = sessionSummaries.get("runtime-session")
assert.equal(
  summaryAfterShutdown,
  summaryBeforeShutdown,
  "session_shutdown must not overwrite compaction summary with low-signal metadata",
)
assert.equal(
  (sessionEndCountById.get("runtime-session") ?? 0),
  runtimeEndsBeforeShutdown + 1,
  "session_shutdown must still emit /sessions/{id}/end after compaction",
)

const memSessionSummary = tools.find((item) => item.name === "mem_session_summary")
const summaryToolContext = {
  ...runtimeContext,
  sessionManager: { getSessionFile: () => "/tmp/worktrees/engram/.pi/sessions/summary-session.json" },
}
await memSessionSummary.execute(
  "call-ss",
  { content: "## Goal\nPreserve this high-signal summary" },
  undefined,
  undefined,
  summaryToolContext,
)

const summaryToolBeforeShutdown = sessionSummaries.get("summary-session")
assert.equal(typeof summaryToolBeforeShutdown, "string", "mem_session_summary should persist summary before shutdown")
const summaryToolEndsBeforeShutdown = sessionEndCountById.get("summary-session") ?? 0
await sessionShutdown({ reason: "exit", target: "app" }, summaryToolContext)
const summaryToolAfterShutdown = sessionSummaries.get("summary-session")
assert.equal(
  summaryToolAfterShutdown,
  summaryToolBeforeShutdown,
  "session_shutdown must not overwrite mem_session_summary content",
)
assert.equal(
  (sessionEndCountById.get("summary-session") ?? 0),
  summaryToolEndsBeforeShutdown + 1,
  "session_shutdown must finalize session end even after mem_session_summary",
)

spawnShouldThrow = true
healthAlwaysDown = true
const memContext = tools.find((item) => item.name === "mem_context")
const failedResult = await memContext.execute("call-2", {}, undefined, undefined, runtimeContext)
assert.equal(failedResult.isError, true, "backend failures must return ToolResult errors, not throw")
assert.ok(Array.isArray(failedResult.content), "error ToolResult must still include content")
assert.equal(failedResult.content[0].type, "text")
assert.ok(
  failedResult.content[0].text.includes("Engram auto-start failed for mem_context"),
  "error ToolResult should communicate startup failure",
)
