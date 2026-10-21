import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { once } from "node:events";

// Real child processes and LF framing; never call extension callbacks from the test.
export function child(executable, args, options, parse = line => line) {
  const process = spawn(executable, args, { ...options, stdio: ["pipe", "pipe", "pipe"] });
  const records = [], listeners = new Set();
  let buffer = "", stderr = "", exit, failure;
  const notify = () => { for (const listener of [...listeners]) listener(); };
  process.stdout.setEncoding("utf8"); process.stderr.setEncoding("utf8");
  process.stdout.on("data", data => {
    buffer += data;
    while (buffer.includes("\n")) {
      const index = buffer.indexOf("\n"), line = buffer.slice(0, index).replace(/\r$/, "");
      buffer = buffer.slice(index + 1);
      try { records.push(parse(line)); } catch (error) { failure = error; }
    }
    notify();
  });
  process.stderr.on("data", data => { stderr += data; });
  process.stdin.on("error", error => { failure = error; notify(); });
  process.on("error", error => { failure = error; notify(); });
  const closed = new Promise(resolve => process.on("close", (code, signal) => {
    exit = { code, signal }; notify(); resolve();
  }));
  const wait = (predicate, timeout = 20_000) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => finish(new Error(`Child deadline: ${executable}\n${stderr}`)), timeout);
    const finish = (error, value) => { clearTimeout(timer); listeners.delete(check); error ? reject(error) : resolve(value); };
    const check = () => {
      const value = predicate(records);
      if (value) finish(undefined, value);
      else if (failure || exit) finish(failure || new Error(`Unexpected child exit ${JSON.stringify(exit)}\n${stderr}`));
    };
    listeners.add(check); check();
  });
  let stopping;
  return { records, wait, closed, get stderr() { return stderr; }, get exit() { return exit; },
    send(value) { process.stdin.write(JSON.stringify(value) + "\n"); },
    stop(timeout = 6000) {
      return stopping ??= (async () => {
        let forced = false;
        const timer = setTimeout(() => { forced = true; process.kill(); }, timeout);
        try { if (!exit) process.stdin.end(); await closed; }
        finally { clearTimeout(timer); }
        assert.equal(forced, false, "child did not shut down on EOF");
        assert.deepEqual(exit, { code: 0, signal: null }, stderr);
      })();
    },
  };
}

export async function run(executable, args, options) {
  const process = child(executable, args, options);
  await process.stop(90_000);
  return { stdout: process.records.join("\n"), stderr: process.stderr, exit: process.exit };
}

export function rpc(executable, args, options) {
  const process = child(executable, args, options, JSON.parse);
  let sequence = 0;
  process.command = async (type, params = {}) => {
    const id = String(++sequence);
    process.send({ id, type, ...params });
    const response = await process.wait(records => records.find(record => record.type === "response" && record.id === id));
    assert.equal(response.success, true, JSON.stringify(response));
    return response.data;
  };
  process.tool = async (name, params) => {
    const start = process.records.length;
    await process.command("prompt", { message: "FIXTURE_TOOL " + JSON.stringify({ name, params }) });
    await process.wait(records => records.slice(start).find(record => record.type === "agent_settled"));
    const ends = process.records.slice(start).filter(record => record.type === "tool_execution_end");
    assert.equal(ends.length, 1, JSON.stringify(process.records.slice(start)));
    assert.equal(ends[0].toolName, name);
    return { ...ends[0].result, isError: ends[0].isError ?? false };
  };
  return process;
}

// Synthetic OpenAI-compatible SSE endpoint: the host still dispatches real tools
// and runs its default compaction algorithm, including model-backed summarization.
export async function localProvider() {
  const requests = [];
  const server = createServer(async (req, res) => {
    try {
      assert.equal(req.url, "/v1/chat/completions"); assert.equal(req.method, "POST");
      let text = "";
      for await (const chunk of req) text += chunk;
      const body = JSON.parse(text), last = body.messages.at(-1);
      const content = message => typeof message.content === "string" ? message.content
        : (message.content ?? []).map(part => part.text ?? "").join("");
      // Recovery is an additional model-facing user message, not the command.
      const marker = body.messages.filter(message => message.role === "user")
        .map(content).findLast(text => text.startsWith("FIXTURE_TOOL "));
      const match = body.tools?.length && last.role === "user" && marker
        ? /^FIXTURE_TOOL (.+)$/s.exec(marker) : null;
      const compaction = body.messages.some(message => /summariz|summary/i.test(JSON.stringify(message.content))) && !match && last.role !== "tool";
      requests.push({ path: req.url, compaction, model: body.model, lastRole: last.role });
      const delta = match ? { role: "assistant", tool_calls: [{ index: 0, id: `call_${requests.length}`, type: "function",
        function: { name: JSON.parse(match[1]).name, arguments: JSON.stringify(JSON.parse(match[1]).params) } }] }
        : { role: "assistant", content: compaction ? "Synthetic summary: isolated ownership markers were exercised; continue the lifecycle journey."
          : "Synthetic acknowledgement. " + "Disposable context for actual host compaction. ".repeat(160) };
      const chunk = { id: `fixture_${requests.length}`, object: "chat.completion.chunk", created: 0, model: "fixture",
        choices: [{ index: 0, delta, finish_reason: null }] };
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(`data: ${JSON.stringify(chunk)}\n\n`);
      res.write(`data: ${JSON.stringify({ ...chunk, choices: [{ index: 0, delta: {}, finish_reason: match ? "tool_calls" : "stop" }],
        usage: { prompt_tokens: 3000, completion_tokens: 100, total_tokens: 3100 } })}\n\n`);
      res.end("data: [DONE]\n\n");
    } catch (error) { res.writeHead(500); res.end(String(error)); }
  });
  server.listen(0, "127.0.0.1"); await once(server, "listening");
  return { url: `http://127.0.0.1:${server.address().port}`, requests,
    close: () => new Promise((resolve, reject) => {
      server.close(error => error ? reject(error) : resolve()); server.closeAllConnections();
    }) };
}
