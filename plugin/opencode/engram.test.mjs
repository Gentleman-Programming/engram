import assert from "node:assert/strict"
import { readFileSync, mkdtempSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { createInterface } from "node:readline"
import { createRequire, syncBuiltinESMExports } from "node:module"
import { test } from "node:test"

const require = createRequire(import.meta.url)
const childProcess = require("node:child_process")
const fs = require("node:fs")

const source = readFileSync(new URL("./engram.ts", import.meta.url), "utf8")

let runtimeImport = 0
const PROJECT_ID = "project-1"
const MODEL_SESSION_ID = "model-invented"
const RESOLUTION_ERROR = /could not resolve an authoritative OpenCode runtime session/
const CHILD_SESSIONS = new Map([
  ["leaf", session("leaf", "root")],
  ["root", session("root")],
])

function session(id, parentID, projectID = PROJECT_ID) {
  return { id, ...(parentID === undefined ? {} : { parentID }), projectID }
}

function sdkResult(data, { error, status = 200 } = {}) {
  return { data, error, response: { status } }
}

function missingSDKResult() {
  return sdkResult(undefined, { error: { name: "NotFound" }, status: 404 })
}

function sdkLookup(sessions) {
  return ({ path }) => sdkResult(sessions.get(path.id))
}

function httpResponse(data = { id: "runtime", status: "created" }, ok = true, onJSON, jsonError) {
  return {
    ok,
    status: ok ? 200 : 500,
    async text() {
      onJSON?.()
      if (jsonError) return "{broken"
      return JSON.stringify(data)
    },
    async json() {
      onJSON?.()
      if (jsonError) throw jsonError
      return data
    },
  }
}

function deferredEvent() {
  let emit
  const event = new Promise((resolve) => { emit = resolve })
  return { event, emit }
}

function deferredResponse() {
  const started = deferredEvent()
  const response = deferredEvent()
  return {
    handler() {
      started.emit()
      return response.event
    },
    started: started.event,
    resolve: response.emit,
  }
}

function extractFunctionBody(name) {
  const signature = source.indexOf(`function ${name}`)
  assert.notEqual(signature, -1, `${name} function not found`)
  const bodyStart = source.indexOf("{", signature)
  let depth = 0
  for (let index = bodyStart; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1
    if (source[index] === "}" && --depth === 0) return source.slice(bodyStart + 1, index)
  }
  throw new Error(`${name} function body not found`)
}

function buildEnsureResolvedProjectForTest(resolveProjectName) {
  const body = extractFunctionBody("ensureResolvedProject")
  return new Function("resolveProjectName", `
    let project = "unknown"
    let projectResolutionError = ""
    let projectResolutionGeneration = 0
    let projectAmbiguous = false
    let localReady = true; const ctx = { directory: "/work/engram" }
		async function ensureLocalReady() { return localReady }
    async function ensureResolvedProject() {${body}}
    return {
      ensureResolvedProject,
      state: () => ({ project, projectResolutionError }),
    }
  `)(resolveProjectName)
}

function toolOutput(...sessionIDs) {
  const sessionID = sessionIDs.length === 0 ? MODEL_SESSION_ID : sessionIDs[0]
  return { args: sessionID === undefined ? {} : { session_id: sessionID } }
}

async function assertNoForward(pending, output, error = RESOLUTION_ERROR) {
  assert.notEqual(output.args.session_id, undefined)
  assert.equal(output.args.session_id, MODEL_SESSION_ID)
  await assert.rejects(pending, error)
}

function assertNoRegistration(runtime, message) {
  assert.deepEqual(runtime.registeredIDs, [], message)
}

async function createRuntime(t, {
  directory = "/work/engram",
  adapterPath = "./engram.ts",
  projectCurrentResponse = { project: "engram", project_source: "git_remote" },
	projectCurrentOK = true,
	manifestExists = false,
  identityLookupFails = false,
  emitSpawnError = false,
  emitSpawnExit = false,
  spawnExit = [1, null],
  installBun = true,
  configuredEngramURL,
  realServerFetch = false,
  engramBin,
  engramPort,
  healthOK = true,
  selectLegacyDefault = false,
   sessionGet = async ({ path }) => sdkResult(session(path.id)),
    registrationResponse,
    sessionEndResponse,
    contextResponse,
    nudgeSessionResponse,
    recoveryResponse,
    nudgeObservationsResponse,
    nudgeObservationsError,
} = {}) {
	const originalFetch = globalThis.fetch
	const originalBun = globalThis.Bun
	const originalEngramURL = process.env.ENGRAM_URL
  const originalEngramBin = process.env.ENGRAM_BIN
  const originalEngramPort = process.env.ENGRAM_PORT
  const originalSpawnSync = childProcess.spawnSync
  const originalSpawn = childProcess.spawn
  const originalExistsSync = fs.existsSync
  const registeredIDs = []
  const sessionGetIDs = []
  const requests = []
  const healthURLs = []
	const spawns = []
  const syncSpawns = []
	const startupEvents = []
	if (configuredEngramURL === undefined) delete process.env.ENGRAM_URL
	else process.env.ENGRAM_URL = configuredEngramURL
  if (engramBin === undefined) delete process.env.ENGRAM_BIN
  else process.env.ENGRAM_BIN = engramBin
  if (engramPort === undefined) delete process.env.ENGRAM_PORT
  else process.env.ENGRAM_PORT = engramPort
  if (installBun) {
    globalThis.Bun = {
      spawnSync(args) {
        if (args.includes("remote")) return { exitCode: 1, stdout: Buffer.from("") }
        if (args[1] === "instance-id") return { exitCode: 0, stdout: Buffer.from("00000000000000000000000000000000\n") }
        return { exitCode: 0, stdout: Buffer.from("/work/engram\n") }
      },
      spawn(args, options) {
        spawns.push({ args, options })
        if (args[1] === "sync" && args[2] === "--import") startupEvents.push("import:spawn")
      },
      file() { return { async exists() { return manifestExists } } },
    }
  } else {
    delete globalThis.Bun
  }
  childProcess.spawnSync = (command, args, options) => {
    syncSpawns.push({ args: [command, ...args], options })
    return {
      status: args[0] === "instance-id" && !identityLookupFails ? 0 : 1,
      stdout: args[0] === "instance-id" && !identityLookupFails ? "00000000000000000000000000000000\n" : "",
    }
  }
  childProcess.spawn = (command, args, options) => {
    let errorListener
    let exitListener
    const child = {
      events: [],
      on(event, listener) {
        if (event === "exit") exitListener = listener
        if (event === "error" && typeof listener === "function") {
          this.events.push(event)
          errorListener = listener
        }
        return this
      },
      unref() {
        this.events.push("unref")
        if (emitSpawnExit) queueMicrotask(() => exitListener?.(...spawnExit))
        if (emitSpawnError) queueMicrotask(() => {
          this.events.push("error:emitted")
          errorListener?.(new Error("simulated spawn failure"))
        })
      },
    }
    spawns.push({ args: [command, ...args], options, child })
    if (args[0] === "sync" && args[1] === "--import") startupEvents.push("import:spawn")
    return child
  }
  fs.existsSync = () => manifestExists
  syncBuiltinESMExports()
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname
		if (path === "/health") {
      healthURLs.push(String(url))
      return httpResponse({ status: "ok", instance_id: "00000000000000000000000000000000" }, typeof healthOK === "function" ? healthOK() : healthOK)
    }
    const body = init?.body ? JSON.parse(init.body) : undefined
    requests.push({ path, url: String(url), method: init?.method, body })
		if (path === "/project/current") {
			const response = typeof projectCurrentResponse === "function" ? projectCurrentResponse() : projectCurrentResponse
			return httpResponse(response, projectCurrentOK, () => startupEvents.push("project-current:response"))
		}
    if (path === "/sessions") {
      registeredIDs.push(body.id)
      if (registrationResponse) return registrationResponse(registeredIDs.length, body.id)
      if (realServerFetch) return originalFetch(url, init)
      return httpResponse({ id: body.id, status: "created" })
    }
    if (realServerFetch) return originalFetch(url, init)
    if (path.startsWith("/sessions/") && path.endsWith("/end")) {
      if (sessionEndResponse) return sessionEndResponse(requests.filter(({ path }) => path.startsWith("/sessions/") && path.endsWith("/end")).length)
      return httpResponse({ id: decodeURIComponent(path.split("/")[2]), status: "completed" })
    }
    if (path.startsWith("/sessions/") && recoveryResponse) return recoveryResponse(path)
    if (path.startsWith("/sessions/") && nudgeSessionResponse) return httpResponse(nudgeSessionResponse)
    if (path === "/observations" && (nudgeObservationsResponse !== undefined || nudgeObservationsError)) {
      return httpResponse(nudgeObservationsResponse, true, undefined, nudgeObservationsError)
    }
    if (path === "/context/compaction" && contextResponse) return contextResponse()
    return httpResponse({})
  }

	t.after(() => {
		globalThis.fetch = originalFetch
		globalThis.Bun = originalBun
		if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
		else process.env.ENGRAM_URL = originalEngramURL
    if (originalEngramBin === undefined) delete process.env.ENGRAM_BIN
    else process.env.ENGRAM_BIN = originalEngramBin
    if (originalEngramPort === undefined) delete process.env.ENGRAM_PORT
    else process.env.ENGRAM_PORT = originalEngramPort
    childProcess.spawnSync = originalSpawnSync
    childProcess.spawn = originalSpawn
    fs.existsSync = originalExistsSync
    syncBuiltinESMExports()
	})
  runtimeImport += 1
  const moduleURL = new URL(`${adapterPath}?sdk-runtime=${runtimeImport}`, import.meta.url)
  const module = await import(moduleURL.href)
  let factory = module.Engram
  if (selectLegacyDefault) {
    assert.equal(typeof module.default, "object", "V1 loader needs a default entrypoint")
    assert.equal(module.default.id, "engram")
    assert.strictEqual(module.default.server, module.Engram)
    assert.equal(typeof module.shouldNudgeForObservations, "function", "named helper remains exported")
    factory = module.default.server
  }
  const plugin = await factory({
    directory,
    project: { id: PROJECT_ID },
    client: {
      session: {
        async get(request) {
          sessionGetIDs.push(request.path.id)
          return sessionGet(request)
        },
      },
    },
  })
  return {
    plugin,
    dispose: plugin.dispose,
    event: (type, info) => plugin.event({ event: { type, properties: { info } } }),
    before: plugin["tool.execute.before"],
    chat: plugin["chat.message"],
    after: plugin["tool.execute.after"],
    compact: plugin["experimental.session.compacting"],
    transform: plugin["experimental.chat.system.transform"],
    registeredIDs,
    sessionGetIDs,
    requests,
    healthURLs,
    syncSpawns,
		spawns,
		startupEvents,
  }
}

for (const body of ["", "   ", "{broken", "null", "{}", '{"id":"wrong","status":"completed"}', '{"id":"runtime","status":"ok"}']) {
  test(`session end acknowledgement rejects ${JSON.stringify(body)} and retries`, async (t) => {
    const runtime = await createRuntime(t, { sessionEndResponse: (count) => count === 1
      ? { ok: true, status: 200, async text() { return body }, async json() { return JSON.parse(body) } }
      : httpResponse({ id: "runtime", status: "completed" }) })
    await runtime.event("session.created", session("runtime"))
    await runtime.event("session.deleted", session("runtime"))
    await runtime.event("session.deleted", session("runtime"))
    assert.equal(runtime.requests.filter(({ path }) => path === "/sessions/runtime/end").length, 2)
    await runtime.event("session.deleted", session("runtime"))
    assert.equal(runtime.requests.filter(({ path }) => path === "/sessions/runtime/end").length, 2)
  })
}

for (const failure of ["HTTP", "transport"]) {
  test(`session end acknowledgement retains ${failure} failure for disposal retry`, async (t) => {
    const runtime = await createRuntime(t, { sessionEndResponse: (count) => {
      if (count > 1) return httpResponse({ id: "runtime", status: "completed" })
      if (failure === "transport") throw new Error("private transport detail")
      return httpResponse({}, false)
    } })
    await runtime.event("session.created", session("runtime"))
    await runtime.event("session.deleted", session("runtime"))
    await runtime.dispose()
    assert.equal(runtime.requests.filter(({ path }) => path === "/sessions/runtime/end").length, 2)
  })
}

for (const kind of ["error", "exit", "signal"]) {
  test(`degraded startup warning retains asynchronous serve ${kind}`, async (t) => {
    const runtime = await createRuntime(t, { healthOK: false, emitSpawnError: kind === "error", emitSpawnExit: kind !== "error", spawnExit: kind === "signal" ? [null, "SIGTERM"] : [1, null] })
    const output = { output: "original" }
    await runtime.after({ tool: "read", sessionID: "runtime" }, output)
    assert.match(output.output, /Engram degraded.*startup/)
    assert.equal(output.output.match(/Engram degraded/g).length, 1)
    assert.ok(output.output.length < 400)
    const next = { output: "next" }
    await runtime.after({ tool: "read", sessionID: "runtime" }, next)
    assert.doesNotMatch(next.output, /startup/)
  })
  test(`degraded import warning retains observable ${kind}`, async (t) => {
    const runtime = await createRuntime(t, { manifestExists: true, emitSpawnError: kind === "error", emitSpawnExit: kind !== "error", spawnExit: kind === "signal" ? [null, "SIGTERM"] : [1, null] })
    const output = { output: "original" }
    await runtime.after({ tool: "read", sessionID: "runtime" }, output)
    assert.match(output.output, /Engram degraded.*import/)
    assert.equal(output.output.match(/Engram degraded/g).length, 1)
    assert.ok(output.output.length < 400)
    const next = { output: "next" }
    await runtime.after({ tool: "read", sessionID: "runtime" }, next)
    assert.equal(next.output, "next")
  })
}

test("degraded import warning ignores normal child exit", async (t) => {
  const runtime = await createRuntime(t, { manifestExists: true, emitSpawnExit: true, spawnExit: [0, null] })
  const output = { output: "original" }
  await runtime.after({ tool: "read", sessionID: "runtime" }, output)
  assert.equal(output.output, "original")
})

test("degraded startup warning ignores normal child exit", async (t) => {
  const runtime = await createRuntime(t, { healthOK: false, emitSpawnExit: true, spawnExit: [0, null] })
  const output = { output: "original" }
  await runtime.after({ tool: "read", sessionID: "runtime" }, output)
  assert.doesNotMatch(output.output, /startup/)
  assert.match(output.output, /readiness/)
})

const runtimeGlobalsBeforeIsolation = {
  spawnSync: childProcess.spawnSync,
  spawn: childProcess.spawn,
  existsSync: fs.existsSync,
  fetch: globalThis.fetch,
}

test("degraded startup warning does not leak across plugin instances", async (t) => {
  await createRuntime(t, { identityLookupFails: true })
  await t.test("healthy instance", async (t) => {
    const healthy = await createRuntime(t)
    const output = { output: "original" }
    await healthy.after({ tool: "read", sessionID: "runtime" }, output)
    assert.equal(output.output, "original")
  })
})

test("runtime mocks restore globals after nested contexts", () => {
  // This separate test runs after both instance contexts have finished teardown.
  assert.strictEqual(childProcess.spawnSync, runtimeGlobalsBeforeIsolation.spawnSync)
  assert.strictEqual(childProcess.spawn, runtimeGlobalsBeforeIsolation.spawn)
  assert.strictEqual(fs.existsSync, runtimeGlobalsBeforeIsolation.existsSync)
  assert.strictEqual(globalThis.fetch, runtimeGlobalsBeforeIsolation.fetch)
})

test("degraded HTTP warning does not conflate expected registration refusal with downtime", async (t) => {
  const runtime = await createRuntime(t, { registrationResponse: () => registrationFailure("session_project_conflict") })
  const output = { output: "original" }
  await runtime.after({ tool: "read", sessionID: "runtime" }, output)
  assert.equal(output.output, "original")
})

for (const category of ["startup", "import", "transport", "HTTP"]) {
  test(`degraded ${category} warning is retained and deduplicated`, async (t) => {
    const runtime = await createRuntime(t, category === "startup" ? { identityLookupFails: true }
      : category === "import" ? { manifestExists: true, emitSpawnError: true } : {})
    if (category === "transport") globalThis.fetch = async () => { throw new Error("private path") }
    if (category === "HTTP") globalThis.fetch = async () => httpResponse({}, false)
    const unknown = { output: 42 }
    await runtime.after({ tool: "read", sessionID: "runtime" }, unknown)
    assert.equal(unknown.output, 42)
    const output = { output: "original", metadata: { keep: true } }
    await runtime.after({ tool: "read", sessionID: "runtime" }, output)
    assert.match(output.output, /Engram degraded/)
    assert.doesNotMatch(output.output, /private path/)
    assert.deepEqual(output.metadata, { keep: true })
    const again = { output: "next" }
    await runtime.after({ tool: "read", sessionID: "runtime" }, again)
    assert.equal(again.output, "next")
  })
}

function registrationFailure(code) {
  return { ...httpResponse({ code }, false), status: 409 }
}

for (const endedCount of [1, 2]) {
  test(`ended roots advance to resume:${endedCount + 1} for every session-bound hook`, async (t) => {
    const runtime = await createRuntime(t, {
      registrationResponse: () => httpResponse({ id: `runtime:resume:${endedCount + 1}`, status: "created" }),
      contextResponse: () => httpResponse({ context: "resumed context" }),
    })
    const effective = `runtime:resume:${endedCount + 1}`
    await runtime.chat({ sessionID: "runtime" }, { parts: [{ type: "text", text: "Continue the previous conversation" }], message: {} })
    await runtime.after({ sessionID: "runtime", tool: "Task" }, "A reusable learning from this completed task that exceeds fifty characters")
    const output = toolOutput()
    await runtime.before({ sessionID: "runtime", tool: "engram_mem_save" }, output)
    assert.equal(output.args.session_id, effective)
    for (const path of ["/prompts", "/observations/passive"]) {
      assert.equal(runtime.requests.find((r) => r.path === path).body.session_id, effective)
    }
    await runtime.compact({ sessionID: "runtime" }, { context: [] })
    assert.equal(new URL(runtime.requests.find((r) => r.path === "/context/compaction").url).searchParams.get("session_id"), effective)
    await runtime.dispose()
    assert.deepEqual(runtime.requests.filter((r) => r.path.endsWith("/end")).map((r) => r.path), [`/sessions/${encodeURIComponent(effective)}/end`])
  })
}

test("concurrent resumed writes share one resume request", async (t) => {
  const runtime = await createRuntime(t, {
    registrationResponse: () => httpResponse({ id: "runtime:resume:2", status: "created" }),
  })
  const outputs = [toolOutput(), toolOutput(), toolOutput()]
  await Promise.all(outputs.map((output) => runtime.before({ sessionID: "runtime", tool: "mem_save" }, output)))
  assert.deepEqual(runtime.registeredIDs, ["runtime"])
  assert.equal(runtime.requests.find((r) => r.path === "/sessions").body.resume, true)
  assert.ok(outputs.every((o) => o.args.session_id === "runtime:resume:2"))
})

for (const code of ["session_project_conflict", "session_already_ended"]) {
  test(`${code} refuses writes with a specific cause and warns once`, async (t) => {
    const warnings = []
    const originalWarn = console.warn
    console.warn = (message) => warnings.push(message)
    t.after(() => { console.warn = originalWarn })
    const runtime = await createRuntime(t, { registrationResponse: () => registrationFailure(code) })
    for (let i = 0; i < 2; i++) {
      const output = toolOutput()
      await assert.rejects(runtime.before({ sessionID: "runtime", tool: "mem_save" }, output), new RegExp(code))
      assert.equal(output.args.session_id, MODEL_SESSION_ID)
    }
    assert.deepEqual(runtime.registeredIDs, ["runtime", "runtime"])
    assert.equal(warnings.length, 1)
    await runtime.dispose()
    assert.equal(runtime.requests.filter((r) => r.path.endsWith("/end")).length, 0, "rejected registrations do not own cleanup")
  })
}

test("renewal advances again when another instance ends the effective session", async (t) => {
  let effective = "runtime:resume:2"
  const runtime = await createRuntime(t, { registrationResponse: () => httpResponse({ id: effective, status: "created" }) })
  const output = toolOutput()
  await runtime.before({ sessionID: "runtime", tool: "mem_save" }, output)
  effective = "runtime:resume:3"
  await runtime.before({ sessionID: "runtime", tool: "mem_save" }, output)
  assert.equal(output.args.session_id, "runtime:resume:3")
})

test("an uncertain renewal failure keeps cleanup ownership of the registered session", async (t) => {
  let failRenewal = false
  const runtime = await createRuntime(t, { registrationResponse: () => failRenewal
    ? httpResponse({ error: "unavailable" }, false) : httpResponse({ id: "runtime:resume:2", status: "created" }) })
  await runtime.before({ sessionID: "runtime", tool: "mem_save" }, toolOutput())
  failRenewal = true
  await assert.rejects(runtime.before({ sessionID: "runtime", tool: "mem_save" }, toolOutput()), /could not confirm/)
  await runtime.dispose()
  assert.deepEqual(runtime.requests.filter((r) => r.path.endsWith("/end")).map((r) => r.path),
    [`/sessions/${encodeURIComponent("runtime:resume:2")}/end`], "a failed renewal must not drop the owned effective session")
})

test("an uncertain initial resume never guesses a cleanup identity", async (t) => {
  const runtime = await createRuntime(t, { registrationResponse: () => httpResponse({}, true, undefined, new Error("lost acknowledgement")) })
  await assert.rejects(runtime.before({ sessionID: "runtime", tool: "mem_save" }, toolOutput()), /could not confirm/)
  await runtime.dispose()
  assert.deepEqual(runtime.registeredIDs, ["runtime"])
  assert.equal(runtime.requests.filter((r) => r.path.endsWith("/end")).length, 0)
})

test("a timed-out registration followed by refusal never guesses an effective ID", async (t) => {
  const runtime = await createRuntime(t, {
    registrationResponse: (attempt) => {
      if (attempt === 1) throw new Error("registration timed out")
      return registrationFailure("session_already_ended")
    },
  })
  for (const error of [/could not confirm/, /session_already_ended/]) {
    const output = toolOutput()
    await assert.rejects(runtime.before({ sessionID: "runtime", tool: "mem_save" }, output), error)
    assert.equal(output.args.session_id, MODEL_SESSION_ID, "no MCP session_id is injected without an acknowledgement")
  }
  await runtime.chat({ sessionID: "runtime" }, { parts: [{ type: "text", text: "Continue the conversation" }], message: {} })
  await runtime.after({ sessionID: "runtime", tool: "Task" }, "A reusable learning from this completed task that exceeds fifty characters")
  assert.ok(runtime.registeredIDs.length >= 2)
  assert.ok(runtime.registeredIDs.every((id) => id === "runtime"), "registration never invents a continuation ID")
  assert.equal(runtime.requests.filter((r) => ["/prompts", "/observations/passive"].includes(r.path)).length, 0, "refused sessions accept no writes")
  await runtime.dispose()
  assert.equal(runtime.requests.filter((r) => r.path.endsWith("/end")).length, 0, "dispose never ends an unacknowledged or guessed ID")
})

test("separate plugin instances converge on the same resumed identity", async (t) => {
  const options = { registrationResponse: () => httpResponse({ id: "runtime:resume:2", status: "created" }) }
  const first = await createRuntime(t, options)
  await t.test("second instance", async (t) => {
    const second = await createRuntime(t, options)
    const outputs = [toolOutput(), toolOutput()]
    await Promise.all([first, second].map((runtime, i) => runtime.before({ sessionID: "runtime", tool: "mem_save" }, outputs[i])))
    assert.ok(outputs.every((o) => o.args.session_id === "runtime:resume:2"))
  })
})

test("unknown refusals and mismatched resumed acknowledgements never advance", async (t) => {
  for (const response of [registrationFailure("other_conflict"), httpResponse({ id: "foreign", status: "created" }), httpResponse({ id: "runtime-other:resume:2", status: "created" }), httpResponse({ id: "runtime:resume:2", status: "rejected" }), httpResponse({}, false)]) {
    await t.test(JSON.stringify(response), async (t) => {
      const runtime = await createRuntime(t, { registrationResponse: () => response })
      await assert.rejects(runtime.before({ sessionID: "runtime", tool: "mem_save" }, toolOutput()), /could not confirm/)
      assert.deepEqual(runtime.registeredIDs, ["runtime"])
    })
  }
})

test("resumed save nudge looks up the effective session", async (t) => {
  const runtime = await createRuntime(t, { registrationResponse: () => httpResponse({ id: "runtime:resume:2", status: "created" }),
    nudgeSessionResponse: { started_at: "2020-01-01 00:00:00" }, nudgeObservationsResponse: [] })
  const output = { system: [] }
  await runtime.transform({ sessionID: "runtime" }, { system: [] })
  assert.equal(runtime.registeredIDs.length, 0, "the nudge never registers a session")
  await runtime.before({ sessionID: "runtime", tool: "mem_save" }, toolOutput())
  await runtime.transform({ sessionID: "runtime" }, output)
  assert.ok(runtime.requests.some((r) => r.path === "/sessions/runtime%3Aresume%3A2"))
  assert.match(output.system.join(""), /MEMORY REMINDER/)
})

test("V1 default selection initializes only the Engram factory once", async (t) => {
  const runtime = await createRuntime(t, { selectLegacyDefault: true })
  assert.equal(typeof runtime.plugin.event, "function")
  assert.equal(runtime.healthURLs.length, 1, "the selected factory runs once")
  assert.equal(runtime.startupEvents.filter((event) => event === "project-current:response").length, 1)
})

for (const adapterPath of ["./engram.ts", "../../internal/setup/plugins/opencode/engram.ts"]) {
  test(`Windows internal launches preserve options and hide consoles: ${adapterPath}`, async (t) => {
    let healthChecks = 0
    const runtime = await createRuntime(t, {
      adapterPath,
      installBun: false,
      engramBin: "custom-engram",
      directory: "C:\\work\\engram",
      manifestExists: true,
      healthOK: () => ++healthChecks > 1,
    })
    assert.deepEqual(runtime.syncSpawns, [{
      args: ["custom-engram", "instance-id"],
      options: { encoding: "utf8", windowsHide: true },
    }])
    assert.deepEqual(runtime.spawns.map(({ args, options }) => ({ args, options })), [
      { args: ["custom-engram", "serve"], options: { detached: true, stdio: "ignore", windowsHide: true } },
      { args: ["custom-engram", "sync", "--import"], options: { cwd: "C:\\work\\engram", detached: true, stdio: "ignore", windowsHide: true } },
    ])
    assert.ok(runtime.spawns.every(({ child }) => child.events.includes("unref")))
    assert.deepEqual(runtime.startupEvents, ["project-current:response", "import:spawn"])
    assert.equal(runtime.healthURLs.length, 2)
    await runtime.event("session.created", session("runtime"))
    assert.deepEqual(runtime.registeredIDs, ["runtime"])
  })

  test(`Windows internal launches remain gated: ${adapterPath}`, async (t) => {
    const runtime = await createRuntime(t, { adapterPath, configuredEngramURL: "http://127.0.0.1:17437" })
    assert.deepEqual(runtime.syncSpawns, [])
    assert.deepEqual(runtime.spawns, [])
    assert.deepEqual(runtime.healthURLs, ["http://127.0.0.1:17437/health"])
  })
}

test("adapter initializes and returns hooks without Bun or ENGRAM_URL", async (t) => {
  const runtime = await createRuntime(t, { installBun: false })

  assert.equal(typeof runtime.plugin.event, "function")
  assert.equal(typeof runtime.plugin["chat.message"], "function")
})

test("adapter treats blank optional Engram environment values as unset at its import boundary", async (t) => {
  for (const scenario of [
    { name: "absent", engramBin: undefined, engramPort: undefined, configuredEngramURL: undefined },
    { name: "empty", engramBin: "", engramPort: "", configuredEngramURL: "" },
    { name: "whitespace-only", engramBin: " \t", engramPort: " \n", configuredEngramURL: " \t" },
    { name: "explicit", engramBin: " /custom/engram ", engramPort: "17437", configuredEngramURL: undefined },
  ]) {
    await t.test(scenario.name, async (t) => {
      const runtime = await createRuntime(t, { ...scenario, healthOK: false })
      const server = runtime.spawns.find(({ args }) => args[1] === "serve")

      assert.equal(server?.args[0], scenario.engramBin?.trim() ? scenario.engramBin : "engram")
      assert.equal(new URL(runtime.healthURLs[0]).port, scenario.engramPort?.trim() ? scenario.engramPort : "7437")
    })
  }
})

test("an explicit OpenCode ENGRAM_URL keeps precedence and its exact value", async (t) => {
  const configuredEngramURL = " http://127.0.0.1:17437"
  const runtime = await createRuntime(t, {
    configuredEngramURL,
    engramBin: "custom-engram",
    engramPort: "18437",
    healthOK: false,
  })

  assert.equal(runtime.spawns.some(({ args }) => args[1] === "serve"), false)
  assert.equal(runtime.healthURLs[0], `${configuredEngramURL}/health`)
})

test("adapter returns hooks when local identity lookup fails", async (t) => {
  const runtime = await createRuntime(t, { installBun: false, identityLookupFails: true })

  assert.equal(typeof runtime.plugin.event, "function")
  assert.equal(typeof runtime.plugin["chat.message"], "function")
})

test("manifest import ignores an asynchronous child launch error", async (t) => {
  const runtime = await createRuntime(t, { manifestExists: true, emitSpawnError: true })
  const imported = runtime.spawns.find(({ args }) => args[1] === "sync" && args[2] === "--import")

  assert.equal(typeof runtime.plugin.event, "function")
  assert.deepEqual(imported?.child.events, ["error", "unref", "error:emitted"])
})

test("server startup ignores an asynchronous child launch error", async (t) => {
  const runtime = await createRuntime(t, { healthOK: false, emitSpawnError: true })
  const server = runtime.spawns.find(({ args }) => args[1] === "serve")

  assert.equal(typeof runtime.plugin.event, "function")
  assert.deepEqual(server?.child.events, ["error", "unref", "error:emitted"])
})

test("save nudge fails closed for malformed and non-array observation responses", async (t) => {
  for (const scenario of [
    { name: "malformed JSON", error: new SyntaxError("unexpected end of JSON input") },
    { name: "non-array JSON", response: { observations: [] } },
    { name: "non-empty observation without timestamp", response: [{}] },
    { name: "non-empty observation with null timestamp", response: [{ created_at: null }] },
    { name: "non-empty observation with non-string timestamp", response: [{ created_at: 42 }] },
  ]) {
    await t.test(scenario.name, async (t) => {
      const runtime = await createRuntime(t, {
        nudgeSessionResponse: { started_at: new Date(Date.now() - 20 * 60 * 1000).toISOString() },
        nudgeObservationsResponse: scenario.response,
        nudgeObservationsError: scenario.error,
      })
      const output = { system: ["base system prompt"] }

      await runtime.transform({ sessionID: "root" }, output)

      assert.doesNotMatch(output.system[0], /MEMORY REMINDER/)
    })
  }
})

test("project identity delegates Windows paths and worktrees to the canonical server", async (t) => {
  for (const scenario of [
    {
      name: "Windows directory basename",
      directory: "C:\\Users\\Blackie",
      response: { project: "blackie", project_source: "dir_basename" },
      expectedProject: "blackie",
		canWrite: true,
    },
    {
      name: "Windows drive root",
      directory: "C:\\",
      response: { project: "C:\\", project_source: "dir_basename" },
		canWrite: false,
    },
    {
      name: "colon-prefixed project name",
      directory: "C:\\worktrees\\compiler",
      response: { project: "c:compiler", project_source: "config" },
      expectedProject: "c:compiler",
      canWrite: true,
    },
    {
      name: "worktree repository identity",
      directory: "C:\\worktrees\\engram-652",
      response: { project: "engram", project_source: "git_remote" },
      expectedProject: "engram",
		canWrite: true,
    },
  ]) {
    await t.test(scenario.name, async (t) => {
      const runtime = await createRuntime(t, {
        directory: scenario.directory,
        projectCurrentResponse: scenario.response,
      })
      const resolution = runtime.requests.find(({ path }) => path === "/project/current")
      assert.ok(resolution, "the plugin must ask the canonical resolver")
      assert.equal(new URL(resolution.url).searchParams.get("cwd"), scenario.directory)

      await runtime.event("session.created", session("runtime"))
      const registration = runtime.requests.find(({ path }) => path === "/sessions")
      const output = { context: [] }
      await runtime.compact({ sessionID: "runtime" }, output)
		if (scenario.canWrite) {
			assert.equal(registration?.body.project, scenario.expectedProject)
			assert.match(output.context.at(-1), new RegExp(`Use project: '${scenario.expectedProject}'`))
		} else {
			assert.equal(registration, undefined)
			assert.match(output.context.at(-1), /Automatic session, prompt, and passive-capture writes remain disabled/)
			assert.doesNotMatch(output.context.at(-1), /Use project: 'unknown'/)
		}
    })
  }
})

test("ambiguous recovery unavailable acknowledgement never registers or captures", async (t) => {
  const runtime = await createRuntime(t, { projectCurrentResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous", available_projects: ["repo-a", "repo-b"] }, recoveryResponse: () => { throw new Error("HTTP store unavailable") } })
  await runtime.before({ tool: "mem_save", sessionID: "root" }, { args: { project: "repo-a", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "token" } })
  await runtime.after({ tool: "mem_save", sessionID: "root" }, { output: "saved" })
  await runtime.chat({ sessionID: "root" }, { message: {}, parts: [{ type: "text", text: "Automatic prompt must not bypass unavailable acknowledgement" }] })
  await runtime.after({ tool: "Task", sessionID: "root" }, "Passive content".repeat(10))
  await runtime.compact({ sessionID: "root" }, { context: [] })
  assert.equal(runtime.requests.some(({ method }) => method === "POST"), false)
  assert.equal(runtime.requests.some(({ path }) => path === "/context/compaction"), false)
})

test("ambiguous recovery preserves resumed effective identity and explicit arguments", async (t) => {
  let registrations = 0
  const runtime = await createRuntime(t, { projectCurrentResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous", available_projects: ["repo-a", "repo-b"] },
    recoveryResponse: (path) => httpResponse({ id: decodeURIComponent(path.split("/")[2]), project: "repo-a", ownership_mode: "project_owned" }),
    registrationResponse: () => ++registrations === 1 ? httpResponse({ id: "root:resume:2", status: "created" }) : httpResponse({}, false) })
  const selection = { project: "repo-a", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "token" }
  await runtime.before({ tool: "mem_save", sessionID: "root" }, { args: { ...selection } })
  await runtime.after({ tool: "mem_save", sessionID: "root" }, { output: "saved" })
  await runtime.chat({ sessionID: "root" }, { message: {}, parts: [{ type: "text", text: "Resume the acknowledged runtime root" }] })
  assert.equal(runtime.requests.find(({ path }) => path === "/prompts")?.body.session_id, "root:resume:2")
  const output = { args: { ...selection, session_id: "invented" } }
  await runtime.before({ tool: "mem_save", sessionID: "root" }, output)
  assert.deepEqual(output.args, { ...selection, session_id: "root:resume:2" })
})

test("ambiguous recovery authorization stays isolated to each runtime root", async (t) => {
  const runtime = await createRuntime(t, { projectCurrentResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous", available_projects: ["repo-a", "repo-b"] }, recoveryResponse: (path) => { const id = decodeURIComponent(path.split("/")[2]); return httpResponse({ id, project: id === "root-a" ? "repo-a" : "repo-b", ownership_mode: "project_owned" }) }, contextResponse: () => httpResponse({ context: "root context" }) })
  await runtime.before({ tool: "mem_save", sessionID: "root-a" }, { args: { project: "repo-a", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "token" } })
  await runtime.after({ tool: "mem_save", sessionID: "root-a" }, { output: "saved" })
  const start = runtime.requests.length
  await runtime.chat({ sessionID: "root-b" }, { message: {}, parts: [{ type: "text", text: "Unselected root must not capture this prompt" }] })
  await runtime.after({ tool: "Task", sessionID: "root-b" }, "Passive content".repeat(10))
  await runtime.compact({ sessionID: "root-b" }, { context: [] })
  assert.equal(runtime.requests.slice(start).some(({ path, method }) => method === "POST" || path === "/context/compaction"), false)
  await runtime.chat({ sessionID: "root-a" }, { message: {}, parts: [{ type: "text", text: "Selected root may capture its own prompt" }] })
  assert.equal(runtime.requests.find(({ path }) => path === "/prompts")?.body.project, "repo-a")
  await runtime.after({ tool: "Task", sessionID: "root-a" }, "Own passive output".repeat(10))
  assert.equal(runtime.requests.find(({ path }) => path === "/observations/passive")?.body.project, "repo-a")
  const context = { context: [] }
  await runtime.compact({ sessionID: "root-a" }, context)
  assert.match(context.context.join("\n"), /Use project: 'repo-a'/)
  await runtime.before({ tool: "mem_save", sessionID: "root-b" }, { args: { project: "repo-b", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "second-token" } })
  await runtime.after({ tool: "mem_save", sessionID: "root-b" }, { output: "saved" })
  await runtime.chat({ sessionID: "root-b" }, { message: {}, parts: [{ type: "text", text: "Separately selected root can capture its own prompt" }] })
  assert.equal(runtime.requests.filter(({ path }) => path === "/prompts").at(-1).body.project, "repo-b")
})

test("ambiguous recovery requires matching HTTP association before automatic capture", async (t) => {
  for (const scenario of [
    { name: "missing", ack: {}, capture: false },
    { name: "different store project", ack: { id: "runtime", project: "other", ownership_mode: "project_owned" }, capture: false },
    { name: "terminal", ack: { id: "runtime", project: "repo-b", ownership_mode: "project_owned", ended_at: "2026-01-01" }, capture: false },
    { name: "matching", ack: { id: "runtime", project: "repo-b", ownership_mode: "project_owned" }, capture: true },
  ]) await t.test(scenario.name, async (t) => {
    const runtime = await createRuntime(t, { projectCurrentResponse: { project: "", project_source: "ambiguous", error_hint: "ambiguous", available_projects: ["repo-a", "repo-b"] }, nudgeSessionResponse: scenario.ack })
    await runtime.before({ tool: "mem_save", sessionID: "runtime" }, { args: { project: "repo-b", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "token" } })
    await runtime.after({ tool: "mem_save", sessionID: "runtime" }, { output: "saved" })
    await runtime.chat({ sessionID: "runtime" }, { message: {}, parts: [{ type: "text", text: "Automatic prompt after explicit recovery" }] })
    assert.equal(runtime.requests.some(({ path }) => path === "/prompts"), scenario.capture)
    if (!scenario.capture) assert.equal(runtime.requests.some(({ path }) => path === "/sessions"), false)
  })
})

test("ambiguous recovery explicit writes retain selection and runtime identity", async (t) => {
  const runtime = await createRuntime(t, {
    projectCurrentResponse: { project: "", project_source: "ambiguous", available_projects: ["repo-a", "repo-b"], error_hint: "ambiguous project" },
    sessions: new Map([["runtime", session("runtime")]]),
  })
  const output = { args: { project: "repo-b", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "mcp-token", session_id: "invented" } }
  await runtime.before({ tool: "mem_save", sessionID: "runtime" }, output)
  assert.deepEqual(output.args, { project: "repo-b", project_choice_reason: "user_selected_after_ambiguous_project", recovery_token: "mcp-token", session_id: "runtime" })
  assert.equal(runtime.requests.some(({ path }) => path === "/sessions"), false)
  await runtime.chat({ sessionID: "runtime" }, { message: {}, parts: [{ type: "text", text: "Long automatic prompt must remain disabled" }] })
  assert.equal(runtime.requests.some(({ path }) => path === "/prompts"), false)
})

test("project identity resolution failures fail closed for automatic writes", async (t) => {
	for (const scenario of [
		{ name: "failed response", response: { error: "unavailable" }, ok: false },
		{ name: "ambiguous response", response: { project: "", error_hint: "ambiguous project", available_projects: ["repo-a", "repo-b"] } },
		{ name: "malformed response", response: {} },
		{ name: "unknown project", response: { project: "unknown", project_source: "dir_basename" } },
	]) {
		await t.test(scenario.name, async (t) => {
			const runtime = await createRuntime(t, {
				projectCurrentResponse: scenario.response,
				projectCurrentOK: scenario.ok ?? true,
			})
			await runtime.event("session.created", session("runtime"))
			await assert.rejects(
				runtime.before({ tool: "mem_save", sessionID: "runtime" }, toolOutput(undefined)),
				/could not resolve a safe project identity/,
			)
			await runtime.chat(
				{ sessionID: "runtime" },
				{ message: {}, parts: [{ type: "text", text: "A sufficiently long root prompt" }] },
			)
			await runtime.after({ tool: "Task", sessionID: "runtime" }, "A".repeat(60))
			const output = { context: [] }
			await runtime.compact({ sessionID: "runtime" }, output)

			assertNoRegistration(runtime)
			assert.equal(runtime.requests.some(({ path }) => path === "/prompts"), false)
			assert.equal(runtime.requests.some(({ path }) => path === "/observations/passive"), false)
			assert.equal(runtime.requests.some(({ path }) => path === "/context/compaction"), false)
			assert.match(output.context.at(-1), /Automatic session, prompt, and passive-capture writes remain disabled/)
		})
	}
})

test("startup import requires a resolved project identity", async (t) => {
	for (const scenario of [
		{ name: "failed", response: { error: "unavailable" }, ok: false, imports: 0, startupEvents: [] },
		{ name: "ambiguous", response: { project: "", error_hint: "ambiguous project", available_projects: ["repo-a", "repo-b"] }, imports: 0, startupEvents: ["project-current:response"] },
		{ name: "resolved", response: { project: "engram", project_source: "git_remote" }, imports: 1, startupEvents: ["project-current:response", "import:spawn"] },
	]) {
		await t.test(scenario.name, async (t) => {
			const runtime = await createRuntime(t, {
				projectCurrentResponse: scenario.response,
				projectCurrentOK: scenario.ok ?? true,
				manifestExists: true,
			})
			const imports = runtime.spawns.filter(({ args }) => args[1] === "sync" && args[2] === "--import")
			assert.equal(imports.length, scenario.imports)
			assert.deepEqual(runtime.startupEvents, scenario.startupEvents)
		})
	}
})

test("project identity retries a failed resolution on later events", async (t) => {
	let recovered = false
	const runtime = await createRuntime(t, {
		projectCurrentResponse: () => recovered
			? { project: "engram", project_source: "git_remote" }
			: { project: "unknown", project_source: "dir_basename" },
	})
	await runtime.event("session.created", session("runtime"))
	assertNoRegistration(runtime)

	recovered = true
	await runtime.event("session.updated", session("runtime"))
	const output = toolOutput(undefined)
	await runtime.before({ tool: "mem_save", sessionID: "runtime" }, output)
	assert.equal(output.args.session_id, "runtime")
	assert.deepEqual(runtime.registeredIDs, ["runtime"])
	const registration = runtime.requests.find(({ path }) => path === "/sessions")
	assert.equal(registration?.body.project, "engram")
})

test("disposal prevents registrations waiting for project resolution", async (t) => {
  const resolution = deferredResponse()
  let attempts = 0
  const runtime = await createRuntime(t, {
    projectCurrentResponse: () => ++attempts === 1 ? { project: "unknown" } : resolution.handler(),
  })

  const created = runtime.event("session.created", session("runtime"))
  await resolution.started
  const disposed = runtime.dispose()
  resolution.resolve({ project: "engram" })
  await Promise.all([created, disposed])

  assertNoRegistration(runtime)
})

test("a stale project resolution failure cannot overwrite a newer success", async () => {
  const stale = deferredEvent()
  let calls = 0
  const resolution = buildEnsureResolvedProjectForTest(() => {
    calls += 1
    return calls === 1 ? stale.event : Promise.resolve({ project: "engram" })
  })

  const first = resolution.ensureResolvedProject()
  const second = resolution.ensureResolvedProject()
  assert.equal(await second, true)
  stale.emit({ project: "unknown", error: "temporary resolver failure" })
  assert.equal(await first, true)
  assert.deepEqual(resolution.state(), { project: "engram", projectResolutionError: "" })
})

test("embedded and distributable OpenCode plugins remain identical", () => {
  const embedded = readFileSync(new URL("../../internal/setup/plugins/opencode/engram.ts", import.meta.url), "utf8")
  assert.equal(embedded, source)
})

test("a later event recovers an explicitly configured server that was not ready at startup", async (t) => {
	let healthy = false
	const runtime = await createRuntime(t, {
		configuredEngramURL: "http://127.0.0.1:7438",
		healthOK: () => healthy,
	})
	healthy = true
	await runtime.event("session.created", session("runtime"))
	assert.deepEqual(runtime.registeredIDs, ["runtime"])
})

test("registration enters the cache only after a successful acknowledgement", async (t) => {
  assert.match(source, /signal: AbortSignal\.timeout\(3000\)/)
  const runtime = await createRuntime(t, {
    registrationResponse: (attempt) => attempt === 1
      ? httpResponse({ error: "unavailable" }, false)
      : httpResponse(),
  })
  await runtime.event("session.created", session("runtime"))
  assert.deepEqual(runtime.registeredIDs, ["runtime"])

  for (const expectedRegistrations of [2, 3]) {
    const output = toolOutput(undefined)
    await runtime.before({ tool: "mem_save", sessionID: "runtime" }, output)
    assert.equal(output.args.session_id, "runtime")
    assert.equal(runtime.registeredIDs.length, expectedRegistrations)
  }
})

test("OpenCode activity renews a cached root session once per activity wave", async (t) => {
  const renewal = deferredResponse()
  const runtime = await createRuntime(t, {
    registrationResponse: (attempt) => attempt === 2 ? renewal.handler() : httpResponse(),
  })
  await runtime.event("session.created", session("runtime"))
  assert.deepEqual(runtime.registeredIDs, ["runtime"], "session.created remains initial registration")

  const message = { message: {}, parts: [{ type: "text", text: "A sufficiently long root prompt" }] }
  const first = runtime.chat({ sessionID: "runtime" }, message)
  const started = await Promise.race([
    renewal.started.then(() => true),
    new Promise((resolve) => setTimeout(() => resolve(false), 25)),
  ])
  try {
    assert.equal(started, true, "cached runtime activity must start a renewal request")
    const second = runtime.chat({ sessionID: "runtime" }, message)
    await Promise.resolve()
    assert.deepEqual(runtime.registeredIDs, ["runtime", "runtime"], "concurrent activity shares the renewal flight")

    renewal.resolve(httpResponse())
    await Promise.all([first, second])
    await runtime.after({ tool: "Task", sessionID: "runtime" }, "A".repeat(60))

    assert.deepEqual(runtime.registeredIDs, ["runtime", "runtime", "runtime"], "later non-Engram tool activity renews before use")
    assert.equal(runtime.requests.filter(({ path }) => path === "/prompts").length, 2)
    assert.equal(runtime.requests.filter(({ path }) => path === "/observations/passive").length, 1)
  } finally {
    renewal.resolve(httpResponse())
    await first
  }
})

test("write tool hook binds only the four attributed writes to authoritative runtime identity", () => {
  assert.match(source, /SESSION_ATTRIBUTED_WRITE_TOOLS = new Set\(\[[\s\S]*"mem_save"[\s\S]*"mem_save_prompt"[\s\S]*"mem_session_summary"[\s\S]*"mem_capture_passive"/)
  assert.match(source, /"tool.execute.before"/)
  assert.match(source, /output\.args\.session_id = effectiveSessions\.get\(authoritativeSessionID\)!\.id/)
  assert.doesNotMatch(source, /delete output\.args\.session_id/)
  assert.match(source, /throw new Error/)
  assert.doesNotMatch(source, /knownSessions\.add\(sessionId\)[\s\S]{0,160}await engramFetch\("\/sessions"/)
})

test("qualified Engram write IDs inject the authoritative root session", async (t) => {
  const runtime = await createRuntime(t, { sessionGet: sdkLookup(CHILD_SESSIONS) })
  for (const { tool, sessionID, expectedSessionID } of [
    { tool: "engram_mem_save", sessionID: "root", expectedSessionID: "root" },
    { tool: "engram_mem_save_prompt", sessionID: "root", expectedSessionID: "root" },
    { tool: "engram_mem_session_summary", sessionID: "leaf", expectedSessionID: "root" },
    { tool: "engram_mem_capture_passive", sessionID: "root", expectedSessionID: "root" },
  ]) {
    const output = toolOutput(undefined)
    await runtime.before({ tool, sessionID }, output)
    assert.equal(output.args.session_id, expectedSessionID)
  }

  assert.deepEqual(runtime.sessionGetIDs, ["root", "leaf"])
  assert.deepEqual(runtime.registeredIDs, ["root", "root", "root", "root"], "a child must renew the authoritative root, never register itself")
})

test("subagent sessions resolve to the authoritative parent and never register themselves", () => {
  assert.match(source, /parentSessions\.set\(sessionId, parentID\)/)
  assert.match(source, /resolveAuthoritativeSessionID/)
  assert.match(source, /client\.session\.get/)
})

test("fresh plugin resolves a persisted top-level session through the SDK", async (t) => {
  const runtime = await createRuntime(t)
  const output = toolOutput()
  await runtime.before({ tool: "mem_save", sessionID: "persisted-root" }, output)

  assert.equal(output.args.session_id, "persisted-root")
  assert.deepEqual(runtime.sessionGetIDs, ["persisted-root"])
  assert.deepEqual(runtime.registeredIDs, ["persisted-root"])
})

test("fresh plugin follows an unobserved child to its persisted root", async (t) => {
  const runtime = await createRuntime(t, { sessionGet: sdkLookup(CHILD_SESSIONS) })
  const output = toolOutput(undefined)
  await runtime.before({ tool: "mem_session_summary", sessionID: "leaf" }, output)

  assert.equal(output.args.session_id, "root")
  assert.deepEqual(runtime.sessionGetIDs, ["leaf", "root"])
  assert.deepEqual(runtime.registeredIDs, ["root"], "the leaf child must never be registered")
})

test("SDK lookup failures remain fail-closed and retryable", async (t) => {
  let attempt = 0
  const runtime = await createRuntime(t, {
    sessionGet: async ({ path }) => {
      attempt += 1
      if (attempt === 1) return missingSDKResult()
      if (attempt === 2) throw new Error("SDK unavailable")
      return sdkResult(session(path.id))
    },
  })
  for (const expectedAttempts of [1, 2]) {
    const output = toolOutput()
    await assertNoForward(runtime.before({ tool: "mem_save_prompt", sessionID: "retry-root" }, output), output)
    assert.equal(runtime.sessionGetIDs.length, expectedAttempts)
    assertNoRegistration(runtime)
  }

  const recovered = toolOutput(undefined)
  await runtime.before({ tool: "mem_save_prompt", sessionID: "retry-root" }, recovered)
  assert.equal(recovered.args.session_id, "retry-root")
  assert.deepEqual(runtime.sessionGetIDs, ["retry-root", "retry-root", "retry-root"])
  assert.deepEqual(runtime.registeredIDs, ["retry-root"])
})

test("missing ancestors abort without registering the observed child", async (t) => {
  let ancestorExists = false
  const runtime = await createRuntime(t, {
    sessionGet: async ({ path }) => path.id === "child"
      ? sdkResult(session("child", "missing"))
      : ancestorExists
        ? sdkResult(session("missing"))
        : missingSDKResult(),
  })
  const output = toolOutput()
  await assertNoForward(runtime.before({ tool: "mem_capture_passive", sessionID: "child" }, output), output)
  assert.deepEqual(runtime.sessionGetIDs, ["child", "missing"])
  assertNoRegistration(runtime)

  ancestorExists = true
  const recovered = toolOutput(undefined)
  await runtime.before({ tool: "mem_capture_passive", sessionID: "child" }, recovered)
  assert.equal(recovered.args.session_id, "missing")
  assert.deepEqual(runtime.sessionGetIDs, ["child", "missing", "child", "missing"])
  assert.deepEqual(runtime.registeredIDs, ["missing"], "a failed chain must not cache or register its leaf")
})

test("invalid, cyclic, and mismatched SDK ownership aborts without registration", async (t) => {
  for (const scenario of [
    {
      name: "invalid session shape",
      start: "malformed",
      sessions: new Map([["malformed", { id: 42, projectID: PROJECT_ID }]]),
      expectedLookups: ["malformed"],
    },
    {
      name: "cyclic parent chain",
      start: "a",
      sessions: new Map([
        ["a", session("a", "b")],
        ["b", session("b", "a")],
      ]),
      expectedLookups: ["a", "b"],
    },
    {
      name: "self-parent chain",
      start: "self",
      sessions: new Map([["self", session("self", "self")]]),
      expectedLookups: ["self"],
    },
    {
      name: "cross-project mismatch",
      start: "foreign",
      sessions: new Map([["foreign", session("foreign", undefined, "project-2")]]),
      expectedLookups: ["foreign"],
    },
    {
      name: "missing project ID",
      start: "unscoped",
      sessions: new Map([["unscoped", { id: "unscoped" }]]),
      expectedLookups: ["unscoped"],
    },
  ]) {
    await t.test(scenario.name, async (t) => {
      const runtime = await createRuntime(t, { sessionGet: sdkLookup(scenario.sessions) })
      const output = toolOutput()
      await assertNoForward(runtime.before({ tool: "mem_save", sessionID: scenario.start }, output), output)
      assert.deepEqual(runtime.sessionGetIDs, scenario.expectedLookups)
      assertNoRegistration(runtime)
    })
  }
})

test("session.updated reparents a known leaf while deletion tombstones dominate SDK lookup", async (t) => {
  const runtime = await createRuntime(t, {
    sessionGet: async () => {
      throw new Error("tombstoned and event-cached sessions must not query the SDK")
    },
  })
  await runtime.event("session.created", session("old-root"))
  await runtime.event("session.created", session("new-root"))
  await runtime.event("session.created", session("leaf", "old-root"))

  const beforeUpdate = toolOutput(undefined)
  await runtime.before({ tool: "mem_save", sessionID: "leaf" }, beforeUpdate)
  assert.equal(beforeUpdate.args.session_id, "old-root")

  await runtime.event("session.updated", session("leaf", "new-root"))
  const afterUpdate = toolOutput(undefined)
  await runtime.before({ tool: "mem_save", sessionID: "leaf" }, afterUpdate)
  assert.equal(afterUpdate.args.session_id, "new-root")
  assert.deepEqual(runtime.registeredIDs, ["old-root", "new-root", "old-root", "new-root"])

  await runtime.event("session.deleted", { id: "new-root" })
  const deleted = toolOutput()
  await assertNoForward(runtime.before({ tool: "mem_save", sessionID: "leaf" }, deleted), deleted)
  assert.deepEqual(runtime.sessionGetIDs, [])
  assert.deepEqual(runtime.registeredIDs, ["old-root", "new-root", "old-root", "new-root"], "deleted descendants must never revive")
})

test("deleting a leaf during its SDK lookup aborts without mutation or registration", async (t) => {
  const lookup = deferredResponse()
  const runtime = await createRuntime(t, { sessionGet: lookup.handler })
  const output = toolOutput()
  const pending = runtime.before({ tool: "mem_save", sessionID: "leaf" }, output)
  await lookup.started

  await runtime.event("session.deleted", { id: "leaf" })
  lookup.resolve(sdkResult(session("leaf")))

  await assertNoForward(pending, output)
  assertNoRegistration(runtime)
})

test("deleting a staged ancestor tombstones its pending SDK descendants across retries", async (t) => {
  const leafLookup = deferredResponse()
  let leafAttempts = 0
  const runtime = await createRuntime(t, {
    sessionGet: async ({ path }) => {
      assert.equal(path.id, "leaf")
      leafAttempts += 1
      if (leafAttempts === 1) return leafLookup.handler()
      return sdkResult(session("leaf"))
    },
  })
  const first = toolOutput()
  const pending = runtime.before({ tool: "mem_save", sessionID: "leaf" }, first)
  await leafLookup.started

  await runtime.event("session.deleted", { id: "root" })
  leafLookup.resolve(sdkResult(session("leaf", "root")))
  await assertNoForward(pending, first)

  const retry = toolOutput()
  await assertNoForward(runtime.before({ tool: "mem_save", sessionID: "leaf" }, retry), retry)
  assert.deepEqual(runtime.sessionGetIDs, ["leaf"], "a tombstoned staged leaf must not query the SDK again")
  assertNoRegistration(runtime)
})

test("deleting an already-staged ancestor during root lookup tombstones descendants across retries", async (t) => {
  const rootLookup = deferredResponse()
  let leafAttempts = 0
  const runtime = await createRuntime(t, {
    sessionGet: async ({ path }) => {
      if (path.id === "leaf") {
        leafAttempts += 1
        return leafAttempts === 1
          ? sdkResult(session("leaf", "ancestor"))
          : sdkResult(session("leaf"))
      }
      if (path.id === "ancestor") return sdkResult(session("ancestor", "old-root"))
      assert.equal(path.id, "old-root")
      return rootLookup.handler()
    },
  })
  const first = toolOutput()
  const pending = runtime.before({ tool: "mem_save", sessionID: "leaf" }, first)
  await rootLookup.started

  await runtime.event("session.deleted", { id: "ancestor" })
  rootLookup.resolve(sdkResult(session("old-root")))
  await assertNoForward(pending, first)

  const retry = toolOutput()
  await assertNoForward(runtime.before({ tool: "mem_save", sessionID: "leaf" }, retry), retry)
  assert.deepEqual(runtime.sessionGetIDs, ["leaf", "ancestor", "old-root"], "a descendant of the deleted staged ancestor must not query the SDK again")
  assertNoRegistration(runtime)
})

test("a reparent event during leaf lookup overrides the stale SDK parent", async (t) => {
  const lookup = deferredResponse()
  const runtime = await createRuntime(t, { sessionGet: lookup.handler })
  const output = toolOutput(undefined)
  const pending = runtime.before({ tool: "mem_session_summary", sessionID: "leaf" }, output)
  await lookup.started

  await runtime.event("session.updated", session("new-root"))
  await runtime.event("session.updated", session("leaf", "new-root"))
  lookup.resolve(sdkResult(session("leaf", "old-root")))
  await pending

  assert.equal(output.args.session_id, "new-root")
  assert.deepEqual(runtime.sessionGetIDs, ["leaf"])
  assert.deepEqual(runtime.registeredIDs, ["new-root"])
})

test("an ancestor event during a later lookup aborts stale binding without overwriting ownership", async (t) => {
  const rootLookup = deferredResponse()
  const sessions = new Map([
    ["leaf", session("leaf", "ancestor")],
    ["ancestor", session("ancestor", "old-root")],
  ])
  const runtime = await createRuntime(t, {
    sessionGet: async ({ path }) => {
      if (path.id !== "old-root") return sdkResult(sessions.get(path.id))
      return rootLookup.handler()
    },
  })
  const first = toolOutput(undefined)
  const pending = runtime.before({ tool: "mem_save_prompt", sessionID: "leaf" }, first)
  await rootLookup.started

  await runtime.event("session.updated", session("new-root"))
  await runtime.event("session.updated", session("ancestor", "new-root"))
  rootLookup.resolve(sdkResult(session("old-root")))
  await assert.rejects(pending, RESOLUTION_ERROR)
  assert.equal(first.args.session_id, undefined)

  const second = toolOutput(undefined)
  await runtime.before({ tool: "mem_save_prompt", sessionID: "leaf" }, second)
  assert.equal(second.args.session_id, "new-root")
  assert.deepEqual(runtime.registeredIDs, ["old-root", "new-root"])
})

test("write tool hook revalidates leaf and ancestor ownership after registration", async (t) => {
  for (const scenario of [
    {
      name: "leaf reparented",
      mutate: async (runtime) => {
        await runtime.event("session.updated", session("new-root"))
        await runtime.event("session.updated", session("leaf", "new-root"))
      },
    },
    {
      name: "ancestor reparented",
      mutate: async (runtime) => {
        await runtime.event("session.updated", session("new-root"))
        await runtime.event("session.updated", session("ancestor", "new-root"))
      },
    },
    {
      name: "leaf deleted",
      mutate: (runtime) => runtime.event("session.deleted", { id: "leaf" }),
    },
    {
      name: "root ancestor deleted",
      mutate: (runtime) => runtime.event("session.deleted", { id: "old-root" }),
    },
  ]) {
    await t.test(scenario.name, async (t) => {
      const registration = deferredResponse()
      const runtime = await createRuntime(t, {
        sessionGet: async () => {
          throw new Error("event-cached ownership must not query the SDK")
        },
        registrationResponse: registration.handler,
      })
      await runtime.event("session.updated", session("old-root"))
      await runtime.event("session.updated", session("ancestor", "old-root"))
      await runtime.event("session.updated", session("leaf", "ancestor"))

      const output = toolOutput()
      const pending = runtime.before({ tool: "mem_save", sessionID: "leaf" }, output)
      await registration.started
      const mutation = scenario.mutate(runtime)
      registration.resolve(httpResponse({ id: "old-root", status: "created" }))

      await Promise.all([mutation, assertNoForward(pending, output)])
      if (scenario.name === "root ancestor deleted")
        assert.equal(runtime.requests.filter(({ path }) => path === "/sessions/old-root/end").length, 1)
      assert.deepEqual(runtime.registeredIDs, ["old-root"])
      assert.deepEqual(runtime.sessionGetIDs, [])
    })
  }
})

test("chat.message redacts a private block that straddles the truncation limit", async (t) => {
  const runtime = await createRuntime(t)
  const text = `${"a".repeat(1980)}<private>PIN=42</private> trailing`
  await runtime.chat({ sessionID: "runtime" }, { message: {}, parts: [{ type: "text", text }] })

  const prompts = runtime.requests.filter(({ path }) => path === "/prompts")
  assert.equal(prompts.length, 1)
  assert.equal(JSON.stringify(prompts[0].body).includes("PIN=42"), false)
  assert.equal(prompts[0].body.content.includes("[REDACTED]"), true)
})

test("chat.message resolves an unobserved child and skips its prompt", async (t) => {
  const runtime = await createRuntime(t, { sessionGet: sdkLookup(CHILD_SESSIONS) })
  await runtime.chat(
    { sessionID: "leaf" },
    { message: {}, parts: [{ type: "text", text: "A sufficiently long child prompt" }] },
  )

  assert.deepEqual(runtime.sessionGetIDs, ["leaf", "root"])
  assertNoRegistration(runtime, "an unobserved child prompt must not register the child or root")
  assert.equal(runtime.requests.some(({ path }) => path === "/prompts"), false)
})

for (const tool of ["task", "Task"]) {
  test(`delegation passive capture accepts ${tool} and preserves the payload`, async (t) => {
    const runtime = await createRuntime(t, { selectLegacyDefault: true })
    const content = "## Key Learnings:\n- Delegation output should be captured for the acknowledged root session."
    await runtime.after({ tool, sessionID: "runtime" }, content)

    assert.deepEqual(runtime.requests.filter(({ path }) => path === "/observations/passive"), [{
      path: "/observations/passive",
      url: "http://127.0.0.1:7437/observations/passive",
      method: "POST",
      body: { session_id: "runtime", content, project: "engram", source: "task-complete" },
    }])
    await runtime.after({ tool: "read", sessionID: "runtime" }, content)
    assert.equal(runtime.requests.filter(({ path }) => path === "/observations/passive").length, 1,
      "non-delegation tool output must remain excluded")
  })
}

test("V1 delegation passive capture extracts text from the tool result object", async (t) => {
  const runtime = await createRuntime(t, { selectLegacyDefault: true })
  const content = "## Key Learnings:\n- The task result text must reach passive capture without wrapper metadata."
  const output = { title: "Delegated task", output: content, metadata: { task_id: "child" } }
  await runtime.after({ tool: "task", sessionID: "runtime" }, output)

  assert.deepEqual(runtime.requests.filter(({ path }) => path === "/observations/passive"), [{
    path: "/observations/passive",
    url: "http://127.0.0.1:7437/observations/passive",
    method: "POST",
    body: { session_id: "runtime", content, project: "engram", source: "task-complete" },
  }])
  assert.deepEqual(output, { title: "Delegated task", output: content, metadata: { task_id: "child" } },
    "capture must not mutate the tool result")
})

test("delegation passive capture preserves JSON fallback for other result shapes", async (t) => {
  const runtime = await createRuntime(t, { selectLegacyDefault: true })
  const output = { content: "A reusable delegation learning that exceeds the passive capture length threshold." }
  await runtime.after({ tool: "task", sessionID: "runtime" }, output)

  assert.deepEqual(runtime.requests.filter(({ path }) => path === "/observations/passive"), [{
    path: "/observations/passive",
    url: "http://127.0.0.1:7437/observations/passive",
    method: "POST",
    body: { session_id: "runtime", content: JSON.stringify(output), project: "engram", source: "task-complete" },
  }])
})

test("Task passive capture resolves an unobserved child and attributes the root", async (t) => {
  const runtime = await createRuntime(t, { sessionGet: sdkLookup(CHILD_SESSIONS) })
  await runtime.after({ tool: "Task", sessionID: "leaf" }, "A".repeat(60))

  assert.deepEqual(runtime.sessionGetIDs, ["leaf", "root"])
  assert.deepEqual(runtime.registeredIDs, ["root"], "the child must never be registered")
  const passive = runtime.requests.find(({ path }) => path === "/observations/passive")
  assert.equal(passive?.body.session_id, "root")
})

test("compaction resolves an unobserved child and registers only its root", async (t) => {
  const runtime = await createRuntime(t, {
    sessionGet: sdkLookup(CHILD_SESSIONS),
    contextResponse: () => httpResponse({ context: "root-only context" }),
  })
  const output = { context: [] }
  await runtime.compact({ sessionID: "leaf" }, output)

  assert.deepEqual(runtime.sessionGetIDs, ["leaf", "root"])
  assert.deepEqual(runtime.registeredIDs, ["root"], "compaction must never register the child")
  const compactionContext = runtime.requests.find(({ path }) => path === "/context/compaction")
  assert.equal(new URL(compactionContext?.url).searchParams.get("session_id"), "root")
  assert.equal(runtime.requests.some(({ path }) => path === "/context"), false)
  assert.ok(output.context.includes("root-only context"))
  assert.match(output.context.at(-1), /FIRST ACTION REQUIRED/)
})

test("post-compaction protocol relies on injected session-only context", () => {
	const afterCompaction = source.match(/### AFTER COMPACTION[\s\S]*?Do not skip step 1\.[\s\S]*?memory\./)?.[0]
  assert.ok(afterCompaction, "AFTER COMPACTION protocol must exist")
  assert.match(afterCompaction, /session-only compaction context has already been injected/)
  assert.match(afterCompaction, /use it only when explicitly requested/)
  assert.doesNotMatch(afterCompaction, /\d\.\s+(?:Then )?call `mem_context`/)
})

test("compaction skips invalid or missing sessions and still injects recovery context", async (t) => {
  for (const scenario of [
    {
      name: "invalid",
      prepare: (runtime) => runtime.event("session.deleted", { id: "runtime" }),
      sessionGet: async () => { throw new Error("tombstoned sessions must not query the SDK") },
    },
    {
      name: "missing",
      prepare: () => Promise.resolve(),
      sessionGet: async () => missingSDKResult(),
    },
  ]) {
    await t.test(scenario.name, async (t) => {
      const runtime = await createRuntime(t, { sessionGet: scenario.sessionGet })
      await scenario.prepare(runtime)
      const output = { context: [] }
      await runtime.compact({ sessionID: "runtime" }, output)

      assertNoRegistration(runtime)
      assert.equal(runtime.requests.some(({ path }) => path === "/context/compaction" || path === "/context"), false)
      assert.match(output.context.at(-1), /FIRST ACTION REQUIRED/)
    })
  }
})

test("compaction fails closed when the session context endpoint is unavailable", async (t) => {
  const runtime = await createRuntime(t, {
    contextResponse: () => httpResponse({ context: "must not inject" }, false),
  })
  const output = { context: [] }
  await runtime.compact({ sessionID: "runtime" }, output)

  assert.equal(runtime.requests.filter(({ path }) => path === "/context/compaction").length, 1)
  assert.equal(runtime.requests.some(({ path }) => path === "/context"), false)
  assert.equal(output.context.includes("must not inject"), false)
})

test("automatic hooks omit writes when ownership changes during registration", async (t) => {
  for (const scenario of [
    {
      name: "chat.message",
      invoke: ({ chat }) => chat(
        { sessionID: "runtime" },
        { message: {}, parts: [{ type: "text", text: "A sufficiently long root prompt" }] },
      ),
      forbiddenPath: "/prompts",
    },
    {
      name: "Task passive capture",
      invoke: ({ after }) => after({ tool: "Task", sessionID: "runtime" }, "A".repeat(60)),
      forbiddenPath: "/observations/passive",
    },
  ]) {
    await t.test(scenario.name, async (t) => {
      const registration = deferredResponse()
      const runtime = await createRuntime(t, { registrationResponse: registration.handler })
      await runtime.event("session.updated", session("runtime"))
      const pending = scenario.invoke(runtime)
      await registration.started
      await runtime.event("session.updated", session("new-root"))
      const reparented = runtime.event("session.updated", session("runtime", "new-root"))
      registration.resolve(httpResponse())
      await Promise.all([pending, reparented])

      assert.deepEqual(runtime.registeredIDs, ["runtime"])
      assert.equal(runtime.requests.filter(({ path }) => path === "/sessions/runtime/end").length, 1)
      assert.equal(runtime.requests.some(({ path }) => path === scenario.forbiddenPath), false)
    })
  }
})

test("registration acknowledgement must match each runtime ID and created status", async (t) => {
  for (const acknowledgement of [
    { status: "created" },
    { id: "other", status: "created" },
    { id: "runtime" },
    { id: "runtime", status: "rejected" },
  ]) {
    await t.test(JSON.stringify(acknowledgement), async (t) => {
      const runtime = await createRuntime(t, { registrationResponse: () => httpResponse(acknowledgement) })
      const output = toolOutput()
      await assert.rejects(runtime.before({ tool: "mem_save", sessionID: "runtime" }, output), /could not confirm Engram session registration/)
      assert.equal(output.args.session_id, MODEL_SESSION_ID)
    })
  }
  await t.test("distinct IDs cannot accept swapped acknowledgements", async (t) => {
    const runtime = await createRuntime(t, {
      registrationResponse: (attempt) => httpResponse({ id: attempt === 1 ? "second" : "first", status: "created" }),
    })
    const first = toolOutput()
    const second = toolOutput()
    await assert.rejects(runtime.before({ tool: "mem_save", sessionID: "first" }, first), /could not confirm Engram session registration/)
    await assert.rejects(runtime.before({ tool: "mem_save", sessionID: "second" }, second), /could not confirm Engram session registration/)
    assert.equal(first.args.session_id, MODEL_SESSION_ID)
    assert.equal(second.args.session_id, MODEL_SESSION_ID)
    assert.deepEqual(runtime.registeredIDs, ["first", "second"])
  })
})

test("runtime hook rejects failed bindings, retries registration, and binds children to parents", async (t) => {
  const runtime = await createRuntime(t, {
    sessionGet: async ({ path }) => ["unresolved", "orphan"].includes(path.id)
      ? missingSDKResult()
      : sdkResult(session(path.id)),
    registrationResponse: (attempt) => attempt === 1
      ? httpResponse({ error: "unavailable" }, false)
      : httpResponse(),
  })
  const first = toolOutput()
  let registrationErrorMessage = ""
  await assert.rejects(runtime.before({ tool: "mem_save", sessionID: "runtime" }, first), (error) => {
    registrationErrorMessage = error.message
    assert.match(error.message, /could not confirm Engram session registration/)
    assert.match(error.message, /verify that the Engram server is available and retry/)
    return true
  })
  assert.equal(first.args.session_id, MODEL_SESSION_ID, "failed registration must not forward MCP arguments")

  const second = toolOutput()
  await runtime.before({ tool: "mem_save", sessionID: "runtime" }, second)
  assert.equal(second.args.session_id, "runtime")
  assert.deepEqual(runtime.registeredIDs, ["runtime", "runtime"])

  await runtime.event("session.created", session("sub", "runtime"))
  const subagent = toolOutput("sub")
  await runtime.before({ tool: "mem_session_summary", sessionID: "sub" }, subagent)
  assert.equal(subagent.args.session_id, "runtime")
  assert.equal(runtime.registeredIDs.length, 3, "child must renew the confirmed parent, not register itself")

  const unresolved = toolOutput()
  let resolutionErrorMessage = ""
  await assert.rejects(runtime.before({ tool: "mem_capture_passive", sessionID: "unresolved" }, unresolved), (error) => {
    resolutionErrorMessage = error.message
    assert.match(error.message, RESOLUTION_ERROR)
    return true
  })
  assert.notEqual(resolutionErrorMessage, registrationErrorMessage)
  assert.equal(unresolved.args.session_id, MODEL_SESSION_ID, "failed resolution must not forward MCP arguments")
  assert.equal(runtime.registeredIDs.length, 3)

  await runtime.event("session.created", { id: "orphan", parentID: "" })
  const orphan = toolOutput(undefined)
  await assert.rejects(runtime.before({ tool: "mem_capture_passive", sessionID: "orphan" }, orphan), RESOLUTION_ERROR)
  await runtime.event("session.updated", session("orphan", "runtime"))
  await runtime.before({ tool: "mem_capture_passive", sessionID: "orphan" }, orphan)
  assert.equal(orphan.args.session_id, "runtime", "a later authoritative mapping must remain retryable")
  assert.equal(runtime.registeredIDs.length, 4)
})

test("a title-only session.created event registers an authoritative root", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", { ...session("legitimate-root"), title: "Task (legitimate subagent)" })

  const output = toolOutput(undefined)
  await runtime.before({ tool: "mem_capture_passive", sessionID: "legitimate-root" }, output)
  assert.equal(output.args.session_id, "legitimate-root")
  assert.deepEqual(runtime.registeredIDs, ["legitimate-root", "legitimate-root"])
  assert.deepEqual(runtime.sessionGetIDs, [], "event-cached roots must not query the SDK")
})

test("deleting a registered root ends its encoded Engram session before invalidation", async (t) => {
  const sessionID = "root/with space"
  const runtime = await createRuntime(t)
  await runtime.event("session.created", session(sessionID))
  await runtime.event("session.deleted", { id: sessionID })

  const endRequests = runtime.requests.filter(({ path }) => path.startsWith("/sessions/") && path.endsWith("/end"))
  assert.equal(endRequests.length, 1)
  assert.equal(endRequests[0].method, "POST")
  assert.equal(endRequests[0].path, `/sessions/${encodeURIComponent(sessionID)}/end`)

  const output = toolOutput()
  await assertNoForward(runtime.before({ tool: "mem_save", sessionID }, output), output)
})

test("deleting a child never ends the child or its root Engram session", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", session("root"))
  await runtime.event("session.created", session("child", "root"))
  await runtime.event("session.deleted", { id: "child" })

  assert.equal(runtime.requests.some(({ path }) => path.startsWith("/sessions/") && path.endsWith("/end")), false)
})

test("failed root session end retries on a duplicate deletion without confirming closure", async (t) => {
  const runtime = await createRuntime(t, {
    sessionEndResponse: (attempt) => attempt === 1
      ? httpResponse({ error: "unavailable" }, false)
      : httpResponse({ id: "root", status: "completed" }),
  })
  await runtime.event("session.created", session("root"))
  await runtime.event("session.deleted", { id: "root" })
  await runtime.event("session.deleted", { id: "root" })

  const endRequests = runtime.requests.filter(({ path }) => path === "/sessions/root/end")
  assert.equal(endRequests.length, 2)
})

test("duplicate deletion does not repeat a confirmed root session end", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", session("root"))
  await runtime.event("session.deleted", { id: "root" })
  await runtime.event("session.deleted", { id: "root" })

  const endRequests = runtime.requests.filter(({ path }) => path === "/sessions/root/end")
  assert.equal(endRequests.length, 1)
})

test("deleting a parent invalidates descendants and prevents later writes or re-registration", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", session("parent"))
  await runtime.event("session.created", session("child", "parent"))
  await runtime.event("session.created", session("grandchild", "child"))

  const confirmed = toolOutput(undefined)
  await runtime.before({ tool: "mem_save", sessionID: "grandchild" }, confirmed)
  assert.equal(confirmed.args.session_id, "parent")
  await runtime.event("session.deleted", { id: "parent" })

  for (const sessionID of ["child", "grandchild"]) {
    const output = toolOutput()
    await assertNoForward(runtime.before({ tool: "mem_save_prompt", sessionID }, output), output)
    await runtime.event("session.created", session(sessionID))
    await assert.rejects(runtime.before({ tool: "mem_session_summary", sessionID }, toolOutput(undefined)), RESOLUTION_ERROR)
  }

  assert.deepEqual(runtime.registeredIDs, ["parent", "parent"], "invalid descendants must never re-register as top-level sessions")
})

test("plugin disposal closes registered roots, not children, and waits for session ends", async (t) => {
  const rootID = "root/with space"
  const end = deferredResponse()
  const runtime = await createRuntime(t, { sessionEndResponse: end.handler })
  await runtime.event("session.created", session(rootID))
  await runtime.event("session.created", session("child", rootID))

  let settled = false
  const pending = runtime.dispose().then(() => { settled = true })
  await end.started
  await Promise.resolve()
  assert.equal(settled, false)
  end.resolve(httpResponse({ id: rootID, status: "completed" }))
  await pending

  const endRequests = runtime.requests.filter(({ path }) => path.startsWith("/sessions/") && path.endsWith("/end"))
  assert.equal(endRequests.length, 1)
  assert.equal(endRequests[0].path, `/sessions/${encodeURIComponent(rootID)}/end`)
})

// OpenCode owns the hook, not the external MCP tool wrapper. Forwarding below models only
// that wrapper's HTTP POST after the exported hook has rewritten its arguments.
test("OpenCode host bindings survive real server persistence and replace ended registration", async (t) => {
  const dir = mkdtempSync(join(tmpdir(), "engram-opencode-real-"))
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }))
  t.after(() => assert.equal(fs.existsSync(dir), false, `test directory remains: ${dir}`))
  const executable = join(dir, process.platform === "win32" ? "real-server.exe" : "real-server")
  const build = childProcess.spawnSync("go", ["build", "-o", executable, "./plugin/opencode/test/support/real-server"], {
    cwd: new URL("../..", import.meta.url), timeout: 60000, encoding: "utf8",
  })
  assert.ifError(build.error)
  assert.equal(build.status, 0, build.stderr)
  const child = childProcess.spawn(executable, [join(dir, "store")], {
    env: { ...process.env, HOME: dir, ENGRAM_DATA_DIR: join(dir, "data"), ENGRAM_CLOUD_AUTOSYNC: "0" },
    stdio: ["pipe", "pipe", "pipe"],
  })
  let stderr = ""
  child.stderr.setEncoding("utf8").on("data", chunk => { stderr += chunk })
  const lines = createInterface({ input: child.stdout })
  async function bounded(promise, message, ms = 5000) {
    let timer
    try {
      return await Promise.race([promise, new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error(`${message}: ${stderr}`)), ms)
      })])
    } finally { clearTimeout(timer) }
  }
  let shutdownError
  try {
    const url = await new Promise((resolve, reject) => {
      let settled = false
      const cleanup = () => {
        clearTimeout(timeout)
        lines.off("line", onLine)
        child.off("error", onError)
        child.off("exit", onExit)
      }
      const settle = (finish, value) => {
        if (settled) return
        settled = true
        cleanup()
        finish(value)
      }
      const onLine = line => settle(resolve, line)
      const onError = error => settle(reject, error)
      const onExit = code => settle(reject, new Error(`server exited ${code}: ${stderr}`))
      const timeout = setTimeout(() => settle(reject, new Error(`server startup timed out: ${stderr}`)), 60000)
      lines.on("line", onLine)
      child.on("error", onError)
      child.on("exit", onExit)
    })
    const runtime = await createRuntime(t, {
      configuredEngramURL: url, realServerFetch: true, directory: dir,
      sessionGet: async () => { throw new Error("event-cached roots should not query SDK") },
    })
    const ids = ["opencode-host-alpha", "opencode-host-beta"]
    for (const id of ids) await runtime.event("session.created", session(id))
    // The external MCP wrapper is deliberately not imported: direct/manual MCP callers
    // retain their own session semantics; only hook-mediated calls are tested here.
    let observationPosts = 0
    const forward = async (output) => {
      observationPosts += 1
      return fetch(`${url}/observations`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(output.args),
      })
    }
    for (const [index, host] of [ids[0], ids[1], ids[0], ids[1]].entries()) {
      const output = { args: {
        title: `opencode-persisted-${index}`, content: `persistence ${index}`,
        project: "engram", session_id: `model-conflict-${index}`,
      } }
      await runtime.before({ tool: "mem_save", sessionID: host }, output)
      assert.equal(output.args.session_id, host)
      const response = await forward(output)
      assert.equal(response.status, 201, await response.text())
    }
    const persisted = async () => {
      const response = await fetch(`${url}/observations?project=engram&limit=20`)
      assert.equal(response.status, 200)
      return response.json()
    }
    const rows = await persisted()
    assert.equal(rows.length, 4, JSON.stringify(rows))
    for (let index = 0; index < 4; index++) {
      const matching = rows.filter(row => row.title === `opencode-persisted-${index}`)
      assert.equal(matching.length, 1)
      assert.equal(matching[0].session_id, ids[index % 2])
    }
    const ended = await fetch(`${url}/sessions/${ids[0]}/end`, { method: "POST", body: "{}" })
    assert.equal(ended.status, 200)
    const conflict = await fetch(`${url}/sessions`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: ids[0], project: "engram", directory: dir }),
    })
    assert.equal(conflict.status, 409, "the real server refuses ended registration")
    const resumed = { args: { title: "resumed", content: "resumed", session_id: "model-conflict" } }
    const postsBefore = observationPosts
    await runtime.before({ tool: "mem_save", sessionID: ids[0] }, resumed)
    assert.equal(resumed.args.session_id, `${ids[0]}:resume:2`)
    assert.equal(observationPosts, postsBefore, "registration does not itself forward an observation")
    assert.equal((await persisted()).length, 4)
  } finally {
    lines.close()
    if (child.exitCode === null && child.signalCode === null && child.pid) {
      const exited = new Promise(resolve => child.once("exit", resolve))
      child.stdin.end()
      try { await bounded(exited, "server shutdown timed out") }
      catch (error) {
        shutdownError = error
        child.kill()
        try { await bounded(exited, "server kill timed out") }
        catch (killError) { shutdownError = killError }
      }
    }
  }
  if (shutdownError) throw shutdownError
})
