import assert from "node:assert/strict";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { withPluginSandbox } from "./plugin-sandbox.mjs";

// gentle-shell#2468: a server spawned detached from the Pi process outlives the
// session, and Gentle Shell removes the session scratch (a `gpXXXXXX` directory
// under the real temp, a subagent's `gaXXXXXX` under it) when the session ends.
// A server whose inherited temp was deleted fails every SQLite write on Windows
// with CANTOPEN (14) while reads stay healthy, so the detached serve spawn must
// drop the temp variables named as a session scratch and keep every other value.
test("the detached serve environment drops session-scratch temp variables and keeps the rest", async () => {
  await withPluginSandbox("engram-scratch-temp-", async ({ sandbox }) => {
    const { detachedServeEnv } = await import(pathToFileURL(join(sandbox, "index.ts")).href);
    const scratch = "/tmp/session/gpAb12cd";
    const env = detachedServeEnv({
      TMPDIR: scratch,
      TMP: scratch,
      TEMP: scratch,
      PATH: "/usr/bin",
      ENGRAM_URL: "http://127.0.0.1:7437",
    });
    for (const key of ["TMPDIR", "TMP", "TEMP"]) assert.equal(env[key], undefined, key);
    assert.equal(env.PATH, "/usr/bin");
    assert.equal(env.ENGRAM_URL, "http://127.0.0.1:7437");
    // Cloud autosync stays part of the detached serve environment.
    assert.equal(env.ENGRAM_CLOUD_AUTOSYNC, "1");

    // A temp the user or the OS chose is never second-guessed, including a
    // subagent scratch (gaXXXXXX), which is session-scoped the same way.
    assert.equal(detachedServeEnv({ TEMP: "C:\\Users\\u\\AppData\\Local\\Temp" }).TEMP, "C:\\Users\\u\\AppData\\Local\\Temp");
    assert.equal(detachedServeEnv({ TMPDIR: "/tmp/user-temp" }).TMPDIR, "/tmp/user-temp");
    assert.equal(detachedServeEnv({ TEMP: "/tmp/session/gaXY12ab" }).TEMP, undefined);
    // A name that merely resembles a scratch is not one.
    assert.equal(detachedServeEnv({ TEMP: "/tmp/gpAb12cdX" }).TEMP, "/tmp/gpAb12cdX");
    assert.equal(detachedServeEnv({ TEMP: "/tmp/gpAb12cd/inner" }).TEMP, "/tmp/gpAb12cd/inner");
  });
});
