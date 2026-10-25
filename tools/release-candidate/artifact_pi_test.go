package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Only an owned TMP ancestor fixture, never an installed/global dependency.
func artifactPiAncestorFixture(t *testing.T, joined *bool) string {
	t.Helper()
	modules := filepath.Join(os.TempDir(), "node_modules")
	createdModules := os.Mkdir(modules, 0700) == nil
	directory := filepath.Join(modules, "synthetic-2055-ancestor")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "executed")
	for name, body := range map[string]string{
		"package.json": `{"name":"synthetic-2055-ancestor","type":"module","exports":"./index.js"}`,
		"index.js":     `import { writeFileSync } from 'node:fs'; writeFileSync(new URL('./executed', import.meta.url), 'synthetic');`,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if !*joined {
			t.Log("retaining ancestor fixture: no public owned-join proof")
			return
		}
		for _, name := range []string{"executed", "index.js", "package.json"} {
			if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
		}
		if err := os.Remove(directory); err != nil {
			t.Error(err)
		}
		if createdModules {
			if err := os.Remove(modules); err != nil {
				t.Error(err)
			}
		}
	})
	return marker
}

// Synthetic native TypeScript only; no Pi SDK or registration substitution.
const artifactPiFixture = `import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
const marker: string = 'unmodified native TypeScript';
const cwd = process.cwd();
for (let dir = cwd;; dir = dirname(dir)) {
  if (existsSync(join(dir, '.git'))) throw new Error('Git ancestry');
  if (dir === dirname(dir)) break;
}
for (const key of ['HOME','USERPROFILE','APPDATA','LOCALAPPDATA','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_CACHE_HOME','TMP','TEMP','TMPDIR','ENGRAM_DATA_DIR']) {
  if (!process.env[key]?.startsWith(cwd)) throw new Error('owned environment');
}
for (const key of ['NODE_OPTIONS','OPENAI_API_KEY','HTTP_PROXY','ENGRAM_CLOUD_TOKEN','GIT_CONFIG_GLOBAL']) {
  if (process.env[key]) throw new Error('inherited environment');
}
if (!readFileSync(new URL(import.meta.url), 'utf8').includes('const marker: string')) throw new Error('rewritten source');
if (!/^[0-9a-f]{64}$/.test(process.env.ENGRAM_TOKEN ?? '')) throw new Error('private token');
if (process.env.ENGRAM_HTTP_TOKEN !== process.env.ENGRAM_TOKEN || process.env.ENGRAM_STARTUP_POLICY !== 'deny') throw new Error('fixed private transport policy');
const response = await fetch(new URL('/health', process.env.ENGRAM_URL), { headers: { Authorization: 'Bearer ' + process.env.ENGRAM_TOKEN } });
const health = await response.json();
if (response.status !== 200 || health.instance_id !== process.env.ENGRAM_EXPECTED_INSTANCE || health.instance_id !== 'synthetic-owned-instance') throw new Error('wrong Core');
console.log('synthetic-private-diagnostic');
if (cwd.includes('-132-')) throw new Error('synthetic import failure');
if (cwd.includes('-133-')) writeFileSync(new URL(import.meta.url), '// mutated');
if (cwd.includes('-135-')) await import('synthetic-2055-ancestor');
if (cwd.includes('-134-')) await new Promise(() => { setInterval(() => {}, 1000); });
export default marker;
`
