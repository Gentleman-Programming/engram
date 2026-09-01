import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { test } from "node:test"

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

function httpResponse(data = { status: "created" }, ok = true, onJSON) {
  return { ok, async json() { onJSON?.(); return data } }
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
    const ctx = { directory: "/work/engram" }
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
  projectCurrentResponse = { project: "engram", project_source: "git_remote" },
	projectCurrentOK = true,
  manifestExists = false,
   sessionGet = async ({ path }) => sdkResult(session(path.id)),
   registrationResponse,
   sessionEndResponse,
   contextResponse,
} = {}) {
  const originalFetch = globalThis.fetch
  const originalBun = globalThis.Bun
  const registeredIDs = []
  const sessionGetIDs = []
  const requests = []
	const spawns = []
	const startupEvents = []
  globalThis.Bun = {
    spawnSync(args) {
      if (args.includes("remote")) return { exitCode: 1, stdout: Buffer.from("") }
      return { exitCode: 0, stdout: Buffer.from("/work/engram\n") }
    },
		spawn(args, options) {
			spawns.push({ args, options })
			if (args[1] === "sync" && args[2] === "--import") startupEvents.push("import:spawn")
		},
		file() { return { async exists() { return manifestExists } } },
  }
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname
    if (path === "/health") return { ok: true, async json() { return { status: "ok" } } }
    const body = init?.body ? JSON.parse(init.body) : undefined
    requests.push({ path, url: String(url), method: init?.method, body })
		if (path === "/project/current") {
			const response = typeof projectCurrentResponse === "function" ? projectCurrentResponse() : projectCurrentResponse
			return httpResponse(response, projectCurrentOK, () => startupEvents.push("project-current:response"))
		}
    if (path === "/sessions") {
      registeredIDs.push(body.id)
      if (registrationResponse) return registrationResponse(registeredIDs.length)
      return httpResponse()
    }
    if (path.startsWith("/sessions/") && path.endsWith("/end")) {
      if (sessionEndResponse) return sessionEndResponse(requests.filter(({ path }) => path.startsWith("/sessions/") && path.endsWith("/end")).length)
      return httpResponse({})
    }
    if (path === "/context/compaction" && contextResponse) return contextResponse()
    return httpResponse({})
  }

  t.after(() => {
    globalThis.fetch = originalFetch
    globalThis.Bun = originalBun
  })
  runtimeImport += 1
  const moduleURL = new URL(`./engram.ts?sdk-runtime=${runtimeImport}`, import.meta.url)
  const { Engram } = await import(moduleURL.href)
  const plugin = await Engram({
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
    event: (type, info) => plugin.event({ event: { type, properties: { info } } }),
    before: plugin["tool.execute.before"],
    chat: plugin["chat.message"],
    after: plugin["tool.execute.after"],
    compact: plugin["experimental.session.compacting"],
    registeredIDs,
    sessionGetIDs,
    requests,
		spawns,
		startupEvents,
  }
}

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

test("registration enters the cache only after a successful acknowledgement", async (t) => {
  assert.match(source, /signal: AbortSignal\.timeout\(3000\)/)
  const runtime = await createRuntime(t, {
    registrationResponse: (attempt) => attempt === 1
      ? httpResponse({ error: "unavailable" }, false)
      : httpResponse(),
  })
  await runtime.event("session.created", session("runtime"))
  assert.deepEqual(runtime.registeredIDs, ["runtime"])

  for (const expectedRegistrations of [2, 2]) {
    const output = toolOutput(undefined)
    await runtime.before({ tool: "mem_save", sessionID: "runtime" }, output)
    assert.equal(output.args.session_id, "runtime")
    assert.equal(runtime.registeredIDs.length, expectedRegistrations)
  }
})

test("write tool hook binds only the four attributed writes to authoritative runtime identity", () => {
  assert.match(source, /SESSION_ATTRIBUTED_WRITE_TOOLS = new Set\(\[[\s\S]*"mem_save"[\s\S]*"mem_save_prompt"[\s\S]*"mem_session_summary"[\s\S]*"mem_capture_passive"/)
  assert.match(source, /"tool.execute.before"/)
  assert.match(source, /output\.args\.session_id = authoritativeSessionID/)
  assert.doesNotMatch(source, /delete output\.args\.session_id/)
  assert.match(source, /throw new Error/)
  assert.doesNotMatch(source, /knownSessions\.add\(sessionId\)[\s\S]{0,160}await engramFetch\("\/sessions"/)
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
  assert.deepEqual(runtime.registeredIDs, ["old-root", "new-root"])

  await runtime.event("session.deleted", { id: "new-root" })
  const deleted = toolOutput()
  await assertNoForward(runtime.before({ tool: "mem_save", sessionID: "leaf" }, deleted), deleted)
  assert.deepEqual(runtime.sessionGetIDs, [])
  assert.deepEqual(runtime.registeredIDs, ["old-root", "new-root"], "deleted descendants must never revive")
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
      await scenario.mutate(runtime)
      registration.resolve(httpResponse())

      await assertNoForward(pending, output)
      assert.deepEqual(runtime.registeredIDs, ["old-root"])
      assert.deepEqual(runtime.sessionGetIDs, [])
    })
  }
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
      await runtime.event("session.updated", session("runtime", "new-root"))
      registration.resolve(httpResponse())
      await pending

      assert.deepEqual(runtime.registeredIDs, ["runtime"])
      assert.equal(runtime.requests.some(({ path }) => path === scenario.forbiddenPath), false)
    })
  }
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
  assert.equal(runtime.registeredIDs.length, 2, "child must reuse the confirmed parent, not register itself")

  const unresolved = toolOutput()
  let resolutionErrorMessage = ""
  await assert.rejects(runtime.before({ tool: "mem_capture_passive", sessionID: "unresolved" }, unresolved), (error) => {
    resolutionErrorMessage = error.message
    assert.match(error.message, RESOLUTION_ERROR)
    return true
  })
  assert.notEqual(resolutionErrorMessage, registrationErrorMessage)
  assert.equal(unresolved.args.session_id, MODEL_SESSION_ID, "failed resolution must not forward MCP arguments")
  assert.equal(runtime.registeredIDs.length, 2)

  await runtime.event("session.created", { id: "orphan", parentID: "" })
  const orphan = toolOutput(undefined)
  await assert.rejects(runtime.before({ tool: "mem_capture_passive", sessionID: "orphan" }, orphan), RESOLUTION_ERROR)
  await runtime.event("session.updated", session("orphan", "runtime"))
  await runtime.before({ tool: "mem_capture_passive", sessionID: "orphan" }, orphan)
  assert.equal(orphan.args.session_id, "runtime", "a later authoritative mapping must remain retryable")
  assert.equal(runtime.registeredIDs.length, 2)
})

test("a title-only session.created event registers an authoritative root", async (t) => {
  const runtime = await createRuntime(t)
  await runtime.event("session.created", { ...session("legitimate-root"), title: "Task (legitimate subagent)" })

  const output = toolOutput(undefined)
  await runtime.before({ tool: "mem_capture_passive", sessionID: "legitimate-root" }, output)
  assert.equal(output.args.session_id, "legitimate-root")
  assert.deepEqual(runtime.registeredIDs, ["legitimate-root"])
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
      : httpResponse({}),
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

  assert.deepEqual(runtime.registeredIDs, ["parent"], "invalid descendants must never re-register as top-level sessions")
})
