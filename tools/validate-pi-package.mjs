import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, realpath, rm, stat, writeFile } from "node:fs/promises";
import { delimiter, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const codeRoot = fileURLToPath(new URL("../", import.meta.url));
const help = `Usage: node tools/validate-pi-package.mjs --core-ref refs/tags/vSEMVER --pi-ref refs/tags/pi-vSEMVER

Uses the current directory as the candidate metadata/package root.
Full refs define an ordered pair of independent strict SemVer versions.
Go JSON version means core; piVersion means Pi and binds metadata/pin/archive.
Identity is metadata only, not remote tag/source/digest or compatibility proof.
Go validates refs, metadata and the installer pin. npm pack runs offline with
lifecycle scripts disabled; Go validates the tarball without extraction.
Requires local Go/npm and existing Go caches. Does not install dependencies.
Tarball, npm cache/config and TEMP files are disposable and checkout-local.
Success means local package validation only, not release approval or parity.
The separate rejection-only candidate checker is unchanged. No publish,
version/tag changes, executable overrides or bypass options are supported.
`;

function argumentsFor(args) {
  if (args.length === 1 && args[0] === "--help") return null;
  const refs = new Map();
  for (let i = 0; i < args.length; i += 2) {
    const flag = args[i];
    const value = args[i + 1];
    if (!["--core-ref", "--pi-ref"].includes(flag) || refs.has(flag) || !value || value.startsWith("--")) throw new Error("invalid arguments");
    refs.set(flag, value);
  }
  if (refs.size !== 2) throw new Error("missing refs");
  return ["-core-ref", refs.get("--core-ref"), "-pi-ref", refs.get("--pi-ref")];
}

async function regular(filename) {
  try { return (await stat(filename)).isFile(); } catch { return false; }
}

// Resolve the installed npm JS entrypoint using normal Node/PATH locations.
// In particular, never execute npm.cmd or a shell on Windows. There is no
// model/candidate-provided executable option or npm_execpath override.
async function npmEntrypoint() {
  const sibling = join(dirname(process.execPath), "node_modules/npm/bin/npm-cli.js");
  if (await regular(sibling)) return sibling;
  for (const directory of (process.env.PATH ?? "").split(delimiter).filter(Boolean)) {
    const command = join(directory, process.platform === "win32" ? "npm.cmd" : "npm");
    if (!await regular(command)) continue;
    if (process.platform === "win32") {
      const candidate = join(directory, "node_modules/npm/bin/npm-cli.js");
      if (await regular(candidate)) return candidate;
    } else {
      const candidate = await realpath(command);
      if (candidate.endsWith("/npm/bin/npm-cli.js")) return candidate;
    }
  }
  throw new Error("npm unavailable");
}

function subprocess(executable, args, cwd, env) {
  const result = spawnSync(executable, args, {
    cwd, env, shell: false, encoding: "utf8", timeout: 24_000, maxBuffer: 1024 * 1024,
  });
  if (result.error || result.signal || result.status !== 0) throw new Error("subprocess failed");
  return result.stdout;
}

async function validate(refs) {
  const root = resolve(process.cwd());
  const temporary = await mkdtemp(join(root, ".pi-package-validation-"));
  try {
    const temp = join(temporary, "tmp");
    await mkdir(temp);
    const userconfig = join(temporary, "user.npmrc");
    const globalconfig = join(temporary, "global.npmrc");
    await writeFile(userconfig, "");
    await writeFile(globalconfig, "");
    const env = {
      ...Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^(npm_config_|npm_token$|node_options$)/i.test(key))),
      TEMP: temp, TMP: temp, TMPDIR: temp, NODE_OPTIONS: "",
      GOPROXY: "off", GOTOOLCHAIN: "local", GOSUMDB: "off", GOFLAGS: "-mod=readonly",
      npm_config_cache: join(temporary, "npm-cache"), npm_config_userconfig: userconfig,
      npm_config_globalconfig: globalconfig, npm_config_update_notifier: "false",
      npm_config_offline: "true", npm_config_ignore_scripts: "true",
    };
    const goArgs = ["run", "./tools/release-candidate", "-root", root, ...refs];
    // Go remains the only SemVer/metadata authority; no npm work before this.
    const metadata = JSON.parse(subprocess("go", goArgs, codeRoot, env));
    if (typeof metadata.version !== "string" || typeof metadata.piVersion !== "string") throw new Error("invalid metadata export");
    subprocess(process.execPath, [await npmEntrypoint(), "pack", "--offline", "--ignore-scripts", "--json", "--workspaces=false", "--pack-destination", temporary], join(root, "plugin/pi"), env);
    const tarball = join(temporary, `gentle-engram-${metadata.piVersion}.tgz`);
    const result = JSON.parse(subprocess("go", [...goArgs, "-pi-tarball", tarball], codeRoot, env));
    if (result.validation !== "local-package-only" || result.version !== metadata.version || result.piVersion !== metadata.piVersion || !Number.isInteger(result.files) || result.files < 1) throw new Error("invalid package validation result");
  } finally {
    // Only this invocation's mkdtemp directory, never caller caches/source files.
    await rm(temporary, { recursive: true, force: true });
  }
}

try {
  const refs = argumentsFor(process.argv.slice(2));
  if (refs === null) process.stdout.write(help);
  else {
    await validate(refs);
    process.stdout.write("Local Pi package validated; not release approval.\n");
  }
} catch {
  // Do not forward compiler/npm diagnostics, private paths, or provider output.
  process.stderr.write("Local Pi package validation failed; no release approval.\n");
  process.exitCode = 1;
}
