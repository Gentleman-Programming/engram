import assert from "node:assert/strict"
import { test } from "node:test"

import { Engram } from "./engram.ts"

type SpawnResult = {
  exitCode: number
  stdout?: Buffer
}

async function createPlugin(
  directory: string,
  spawnSync: (command: string[]) => SpawnResult,
) {
  const requests: Array<{ path: string; body?: unknown }> = []

  globalThis.Bun = {
    spawnSync,
    file: () => ({ exists: async () => false }),
  } as typeof Bun

  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(input.toString())
    const body = init?.body ? JSON.parse(init.body.toString()) : undefined
    requests.push({ path: url.pathname, body })

    return new Response(JSON.stringify({}), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  }) as typeof fetch

  const plugin = await Engram({ directory } as never)
  return { plugin, requests }
}

test("uses the Windows basename in compaction recovery outside Git", async () => {
  const { plugin, requests } = await createPlugin("C:\\Users\\Blackie", () => ({ exitCode: 1 }))
  const output = { context: [] as string[] }

  await plugin["experimental.session.compacting"]?.(
    { sessionID: "session-652" } as never,
    output,
  )

  assert.match(output.context.at(-1) ?? "", /Use project: 'Blackie'/)
  assert.doesNotMatch(output.context.at(-1) ?? "", /C:\\Users\\Blackie/)
  assert.deepEqual(
    requests.find(({ path }) => path === "/projects/migrate")?.body,
    { old_project: "C:\\Users\\Blackie", new_project: "Blackie" },
  )
})

test("ignores trailing Windows separators in the basename fallback", async () => {
  const { plugin, requests } = await createPlugin("C:\\Users\\Blackie\\", () => ({ exitCode: 1 }))
  const output = { context: [] as string[] }

  await plugin["experimental.session.compacting"]?.(
    { sessionID: "session-652-trailing" } as never,
    output,
  )

  assert.match(output.context.at(-1) ?? "", /Use project: 'Blackie'/)
  assert.deepEqual(
    requests.find(({ path }) => path === "/projects/migrate")?.body,
    { old_project: "C:\\Users\\Blackie\\", new_project: "Blackie" },
  )
})

test("uses the Windows basename from the Git-root fallback", async () => {
  const { plugin } = await createPlugin("C:\\Users\\Blackie\\project", (command) => {
    if (command.includes("rev-parse")) {
      return {
        exitCode: 0,
        stdout: Buffer.from("C:\\Users\\Blackie\\worktrees\\engram-652\n"),
      }
    }
    return { exitCode: 1 }
  })
  const output = { context: [] as string[] }

  await plugin["experimental.session.compacting"]?.(
    { sessionID: "session-652-git-root" } as never,
    output,
  )

  assert.match(output.context.at(-1) ?? "", /Use project: 'engram-652'/)
})

test("does not migrate when the legacy remote project is unchanged", async () => {
  const { requests } = await createPlugin("C:\\Users\\Blackie", (command) => {
    if (command.includes("get-url")) {
      return {
        exitCode: 0,
        stdout: Buffer.from("https://github.com/Gentleman-Programming/engram.git\n"),
      }
    }
    return { exitCode: 1 }
  })

  assert.equal(requests.some(({ path }) => path === "/projects/migrate"), false)
})

test("migrates the legacy Windows Git-root key from a nested directory", async () => {
  const { requests } = await createPlugin("C:\\repos\\engram\\nested", (command) => {
    if (command.includes("rev-parse")) {
      return {
        exitCode: 0,
        stdout: Buffer.from("C:\\repos\\engram\n"),
      }
    }
    return { exitCode: 1 }
  })

  assert.deepEqual(
    requests.find(({ path }) => path === "/projects/migrate")?.body,
    { old_project: "C:\\repos\\engram", new_project: "engram" },
  )
})

test("preserves POSIX basename fallback behavior", async () => {
  const { plugin } = await createPlugin("/home/blackie", () => ({ exitCode: 1 }))
  const output = { context: [] as string[] }

  await plugin["experimental.session.compacting"]?.(
    { sessionID: "session-posix" } as never,
    output,
  )

  assert.match(output.context.at(-1) ?? "", /Use project: 'blackie'/)
})

test("migrates the legacy empty POSIX key for a trailing separator", async () => {
  const { requests } = await createPlugin("/home/blackie/", () => ({ exitCode: 1 }))

  assert.deepEqual(
    requests.find(({ path }) => path === "/projects/migrate")?.body,
    { old_project: "", new_project: "blackie" },
  )
})
