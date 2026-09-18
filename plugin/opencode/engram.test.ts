import { expect, test } from "bun:test"

const PROJECT = "opencode-archive-test"
const DIRECTORY = "/tmp/opencode-archive-test"
const originalEngramURL = process.env.ENGRAM_URL
process.env.ENGRAM_URL = "http://127.0.0.1:7437"
const { Engram } = await import("./engram")
if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
else process.env.ENGRAM_URL = originalEngramURL

type SessionStatusType = "idle" | "busy" | "retry"

type HarnessOptions = {
  statuses?: Record<string, SessionStatusType>
  statusResponseStyle?: "data" | "fields"
  statusFailure?: boolean
  statusGate?: Promise<void>
  onStatusQuery?: () => void
  sessions?: Record<string, string>
  endStatuses?: number[]
  endFailures?: number
}

type Call = {
  method: string
  path: string
  body?: any
}

type Harness = {
  calls: Call[]
  statusQueries: any[]
}

type Deferred = {
  promise: Promise<void>
  resolve: () => void
}

function deferred(): Deferred {
  let resolve!: () => void
  const promise = new Promise<void>((complete) => {
    resolve = complete
  })
  return { promise, resolve }
}

function sessionInfo(id: string, time: Record<string, unknown> = {}, fields: Record<string, unknown> = {}) {
  return {
    id,
    projectID: "project-id",
    directory: DIRECTORY,
    title: id,
    version: "1.0.0",
    time: { created: 1, updated: 2, ...time },
    ...fields,
  }
}

function archiveEvent(id: string, fields: Record<string, unknown> = {}) {
  return {
    type: "session.updated",
    properties: {
      info: sessionInfo(id, { archived: 1720000000000 }, fields),
    },
  }
}

function idleEvent(id: string) {
  return {
    type: "session.idle",
    properties: { sessionID: id },
  }
}

async function withPlugin<T>(
  options: HarnessOptions,
  run: (plugin: any, harness: Harness) => Promise<T>,
): Promise<T> {
  const originalFetch = globalThis.fetch
  const calls: Call[] = []
  const statusQueries: any[] = []
  const sessionProjects = new Map(Object.entries(options.sessions ?? {}))
  const endStatuses = [...(options.endStatuses ?? [])]
  let endFailures = options.endFailures ?? 0

  globalThis.fetch = async (input, init) => {
    const parsed = new URL(String(input))
    const method = init?.method ?? "GET"
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ method, path: parsed.pathname, body })

    if (parsed.pathname === "/health") return Response.json({ status: "ok" })
    if (parsed.pathname === "/project/current") return Response.json({ project: PROJECT })

    if (parsed.pathname === "/sessions" && method === "POST") {
      if (body?.id && typeof body.project === "string") {
        sessionProjects.set(body.id, body.project)
      }
      return Response.json({ status: "ok" })
    }

    if (parsed.pathname.startsWith("/sessions/")) {
      const suffix = parsed.pathname.slice("/sessions/".length)
      const isEnd = suffix.endsWith("/end")
      const id = decodeURIComponent(isEnd ? suffix.slice(0, -4) : suffix)

      if (isEnd && method === "POST") {
        if (endFailures > 0) {
          endFailures -= 1
          throw new Error("archive end unavailable")
        }
        const status = endStatuses.shift() ?? 200
        return status === 200
          ? Response.json({ status: "ok" })
          : new Response(null, { status })
      }

      if (!sessionProjects.has(id)) {
        return Response.json({ error: "session not found" }, { status: 404 })
      }
      return Response.json({ id, project: sessionProjects.get(id) })
    }

    return Response.json({ status: "ok" })
  }

  const client = {
    session: {
      status: async (request: any = {}) => {
        statusQueries.push(request)
        options.onStatusQuery?.()
        if (options.statusGate) await options.statusGate
        if (options.statusFailure) throw new Error("status unavailable")

        const data = Object.fromEntries(
          Object.entries(options.statuses ?? {}).map(([id, type]) => [id, { type }]),
        )
        if (options.statusResponseStyle === "fields") {
          return { data, error: undefined }
        }
        return data
      },
    },
  }

  try {
    const plugin = await Engram({
      directory: DIRECTORY,
      project: { id: "project-id" },
      client,
    } as any) as any
    return await run(plugin, { calls, statusQueries })
  } finally {
    globalThis.fetch = originalFetch
  }
}

async function runArchiveWithEndStatuses(statuses: number[], endFailures = 0) {
  let error: unknown

  const result = await withPlugin(
    {
      statuses: { "archive-retry": "busy" },
      statusResponseStyle: "fields",
      sessions: { "archive-retry": PROJECT },
      endStatuses: statuses,
      endFailures,
    },
    async (plugin, harness) => {
      try {
        await plugin.event({ event: archiveEvent("archive-retry") })
        await plugin.event({ event: idleEvent("archive-retry") })
      } catch (caught) {
        error = caught
      }
      return harness
    },
  )

  return {
    endCalls: result.calls.filter((call) => call.path === "/sessions/archive-retry/end"),
    error,
  }
}

test("archive while active defers closure until the matching idle event", async () => {
  await withPlugin(
    {
      statuses: { active: "busy" },
      sessions: { active: PROJECT },
    },
    async (plugin, harness) => {
      await plugin.event({ event: archiveEvent("active") })
      expect(harness.calls.some((call) => call.path === "/sessions/active/end")).toBe(false)

      await plugin.event({ event: idleEvent("active") })
      expect(harness.calls.filter((call) => call.path === "/sessions/active/end")).toHaveLength(1)
    },
  )
})

test("inactive archive closes promptly when the status query succeeds", async () => {
  await withPlugin(
    { statuses: { inactive: "idle" }, sessions: { inactive: PROJECT } },
    async (plugin, harness) => {
      await plugin.event({ event: archiveEvent("inactive") })

      expect(harness.statusQueries).toEqual([{ query: { directory: DIRECTORY } }])
      expect(harness.calls.filter((call) => call.path === "/sessions/inactive/end")).toHaveLength(1)
    },
  )
})

test("status query failure defers closure conservatively until idle", async () => {
  await withPlugin(
    {
      statusFailure: true,
      sessions: { unavailable: PROJECT },
    },
    async (plugin, harness) => {
      await plugin.event({ event: archiveEvent("unavailable") })
      expect(harness.calls.some((call) => call.path === "/sessions/unavailable/end")).toBe(false)

      await plugin.event({ event: idleEvent("unavailable") })
      expect(harness.calls.filter((call) => call.path === "/sessions/unavailable/end")).toHaveLength(1)
    },
  )
})

test("ordinary session updates do not trigger archive handling", async () => {
  await withPlugin(
    { sessions: { ordinary: PROJECT } },
    async (plugin, harness) => {
      await plugin.event({
        event: {
          type: "session.updated",
          properties: { info: sessionInfo("ordinary") },
        },
      })

      expect(harness.statusQueries).toHaveLength(0)
      expect(harness.calls.some((call) => call.path.includes("/sessions/ordinary"))).toBe(false)
    },
  )
})

test("updates without archive state preserve deferred archive closure", async () => {
  await withPlugin(
    {
      statuses: { pending: "busy" },
      sessions: { pending: PROJECT },
    },
    async (plugin, harness) => {
      await plugin.event({ event: archiveEvent("pending") })
      await plugin.event({
        event: {
          type: "session.updated",
          properties: { info: sessionInfo("pending") },
        },
      })
      await plugin.event({ event: idleEvent("pending") })

      expect(harness.calls.filter((call) => call.path === "/sessions/pending/end")).toHaveLength(1)
    },
  )
})

test("duplicate archive and idle events result in one closure", async () => {
  await withPlugin(
    {
      statuses: { duplicate: "retry" },
      sessions: { duplicate: PROJECT },
    },
    async (plugin, harness) => {
      await Promise.all([
        plugin.event({ event: archiveEvent("duplicate") }),
        plugin.event({ event: archiveEvent("duplicate") }),
      ])
      expect(harness.calls.some((call) => call.path === "/sessions/duplicate/end")).toBe(false)
      expect(harness.statusQueries).toHaveLength(1)

      await Promise.all([
        plugin.event({ event: idleEvent("duplicate") }),
        plugin.event({ event: idleEvent("duplicate") }),
      ])
      expect(harness.calls.filter((call) => call.path === "/sessions/duplicate/end")).toHaveLength(1)
    },
  )
})

test("subagent, unregistered, and wrong-project sessions are excluded", async () => {
  await withPlugin(
    {
      sessions: { "wrong-project": "other-project" },
    },
    async (plugin, harness) => {
      await plugin.event({
        event: archiveEvent("subagent", { parentID: "parent-session" }),
      })
      await plugin.event({ event: archiveEvent("unregistered") })
      await plugin.event({ event: archiveEvent("wrong-project") })

      expect(harness.calls.some((call) => call.path === "/sessions/subagent/end")).toBe(false)
      expect(harness.calls.some((call) => call.path === "/sessions/unregistered/end")).toBe(false)
      expect(harness.calls.some((call) => call.path === "/sessions/wrong-project/end")).toBe(false)
      expect(harness.calls.some((call) => call.path === "/sessions" && call.body?.id === "unregistered")).toBe(false)
      expect(harness.calls.some((call) => call.path === "/sessions/unregistered")).toBe(true)
      expect(harness.calls.some((call) => call.path === "/sessions/wrong-project")).toBe(true)
    },
  )
})

test("session deletion clears pending archive state", async () => {
  await withPlugin(
    {
      statuses: { deleted: "busy" },
      sessions: { deleted: PROJECT },
    },
    async (plugin, harness) => {
      await plugin.event({ event: archiveEvent("deleted") })
      await plugin.event({
        event: {
          type: "session.deleted",
          properties: { info: sessionInfo("deleted") },
        },
      })
      await plugin.event({ event: idleEvent("deleted") })

      expect(harness.calls.some((call) => call.path === "/sessions/deleted/end")).toBe(false)
    },
  )
})

test("unarchiving before idle cancels deferred closure", async () => {
  await withPlugin(
    {
      statuses: { restored: "busy" },
      sessions: { restored: PROJECT },
    },
    async (plugin, harness) => {
      await plugin.event({ event: archiveEvent("restored") })
      await plugin.event({
        event: {
          type: "session.updated",
          properties: { info: sessionInfo("restored", { archived: 0 }) },
        },
      })
      await plugin.event({ event: idleEvent("restored") })

      expect(harness.calls.some((call) => call.path === "/sessions/restored/end")).toBe(false)
    },
  )
})

for (const [name, cancellationEvent] of [
  ["unarchiving", (id: string) => ({
    type: "session.updated",
    properties: { info: sessionInfo(id, { archived: 0 }) },
  })],
  ["deletion", (id: string) => ({
    type: "session.deleted",
    properties: { info: sessionInfo(id) },
  })],
] as const) {
  test(`${name} invalidates an in-flight archive status check`, async () => {
    const statusStarted = deferred()
    const statusGate = deferred()
    const sessionID = `in-flight-${name}`

    await withPlugin(
      {
        statuses: { [sessionID]: "idle" },
        sessions: { [sessionID]: PROJECT },
        onStatusQuery: statusStarted.resolve,
        statusGate: statusGate.promise,
      },
      async (plugin, harness) => {
        const archive = plugin.event({ event: archiveEvent(sessionID) })
        await statusStarted.promise

        const cancellation = plugin.event({ event: cancellationEvent(sessionID) })
        await Promise.resolve()
        statusGate.resolve()
        await Promise.all([archive, cancellation])

        expect(harness.calls.some((call) => call.path === `/sessions/${sessionID}`)).toBe(false)
        expect(harness.calls.some((call) => call.path === `/sessions/${sessionID}/end`)).toBe(false)
      },
    )
  })
}

test("archive closure succeeds without retry", async () => {
  const result = await runArchiveWithEndStatuses([200])

  expect(result.endCalls).toHaveLength(1)
  expect(result.error).toBeUndefined()
})

test("archive closure retries a conflict once", async () => {
  const result = await runArchiveWithEndStatuses([409, 200])

  expect(result.endCalls).toHaveLength(2)
  expect(result.error).toBeUndefined()
})

test("archive closure retries a server failure once", async () => {
  const result = await runArchiveWithEndStatuses([503, 200])

  expect(result.endCalls).toHaveLength(2)
  expect(result.error).toBeUndefined()
})

test("archive closure retries a rejected end request once", async () => {
  const result = await runArchiveWithEndStatuses([200], 1)

  expect(result.endCalls).toHaveLength(2)
  expect(result.error).toBeUndefined()
})

test("archive closure does not retry non-transient HTTP failures", async () => {
  const result = await runArchiveWithEndStatuses([400])

  expect(result.endCalls).toHaveLength(1)
  expect(result.error).toBeInstanceOf(Error)
  expect((result.error as Error).message).toContain("HTTP 400")
})

test("archive closure reports a failed retry", async () => {
  const result = await runArchiveWithEndStatuses([500, 502])

  expect(result.endCalls).toHaveLength(2)
  expect(result.error).toBeInstanceOf(Error)
  expect((result.error as Error).message).toContain("HTTP 502")
})

test("exhausted rejected archive closure remains deferred for a later idle retry", async () => {
  await withPlugin(
    {
      statuses: { "archive-retry": "busy" },
      statusResponseStyle: "fields",
      sessions: { "archive-retry": PROJECT },
      endFailures: 2,
    },
    async (plugin, harness) => {
      await plugin.event({ event: archiveEvent("archive-retry") })

      let error: unknown
      try {
        await plugin.event({ event: idleEvent("archive-retry") })
      } catch (caught) {
        error = caught
      }

      expect(harness.calls.filter((call) => call.path === "/sessions/archive-retry/end")).toHaveLength(2)
      expect(error).toBeInstanceOf(Error)
      expect((error as Error).message).toContain("archive end unavailable")

      await plugin.event({ event: idleEvent("archive-retry") })
      expect(harness.calls.filter((call) => call.path === "/sessions/archive-retry/end")).toHaveLength(3)
    },
  )
})
