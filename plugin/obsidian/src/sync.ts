import { Notice, Vault } from "obsidian";
import type EngramBrainPlugin from "./main";

interface ExportObservation {
	id: number;
	sync_id: string;
	title: string;
	content: string;
	project?: string | null;
	session_id?: string;
	type: string;
	created_at: string;
	updated_at: string;
}

interface ExportSession {
	id: string;
	project: string;
}

interface ExportData {
	version: string;
	exported_at: string;
	sessions: ExportSession[];
	observations: ExportObservation[];
	prompts: unknown[];
	relations?: unknown[];
}

interface SyncState {
	lastSyncAt: string;
	files: Record<string, string>;
	version: number;
}

const STATE_VERSION = 1;
const STATE_FILENAME = ".engram-sync-state.json";

export interface SyncResult {
	created: number;
	updated: number;
	deleted: number;
	skipped: number;
	total: number;
}

/** Pull a complete snapshot. Missing observations never imply deletion. */
export async function syncNow(plugin: EngramBrainPlugin): Promise<SyncResult> {
	const { settings } = plugin;
	const vault: Vault = plugin.app.vault;
	if (!settings.engramUrl) {
		new Notice("Engram URL is required");
		throw new Error("Engram URL is required");
	}
	const subfolder = settings.vaultSubfolder || "engram";
	if (!isSafeFolder(subfolder)) throw new Error("Invalid vault subfolder");

	// Validate the entire response and derive every path before touching the vault.
	const data = await fetchExport(settings.engramUrl, settings.projectFilter || undefined);
	const notes = data.observations.map(obs => ({
		path: `${subfolder}/observations/observation-${obs.id}.md`,
		content: renderObservation(obs),
	}));
	const stateFile = `${subfolder}/${STATE_FILENAME}`;
	const state = await readState(vault, stateFile);
	const result: SyncResult = { created: 0, updated: 0, deleted: 0, skipped: 0, total: notes.length };
	await ensureFolder(vault, subfolder);
	if (notes.length) await ensureFolder(vault, `${subfolder}/observations`);

	for (const note of notes) {
		const hash = await hashContent(note.content);
		const file = vault.getFileByPath(note.path);
		if (file && state.files[note.path] === hash) {
			result.skipped++;
			continue;
		}
		if (file) {
			await vault.modify(file, note.content);
			result.updated++;
		} else {
			await vault.create(note.path, note.content);
			result.created++;
		}
		state.files[note.path] = hash;
	}
	state.lastSyncAt = data.exported_at;
	state.version = STATE_VERSION;
	await writeState(vault, stateFile, state);
	return result;
}

function isSafeFolder(path: string): boolean {
	return path.length > 0 && path.split("/").every(part =>
		part !== "" && part !== "." && part !== ".." && !/[\\:\x00-\x1f]/.test(part)
	);
}

function renderObservation(obs: ExportObservation): string {
	// Fixed filenames and plain Markdown avoid interpreting untrusted metadata as paths or YAML.
	return `# ${obs.title.replace(/[\r\n]+/g, " ")}\n\n${obs.content}\n`;
}

async function fetchExport(baseUrl: string, project?: string): Promise<ExportData> {
	const url = new URL(`${baseUrl}/export`);
	if (project) url.searchParams.set("project", project);
	else url.searchParams.set("all_projects", "true");
	let res: Response;
	try {
		res = await fetch(url.toString(), { signal: AbortSignal.timeout(30_000) });
	} catch {
		throw new Error("Sync failed: could not reach engram server");
	}
	if (!res.ok) throw new Error(`Sync failed: server returned ${res.status} ${res.statusText}`);
	let data: unknown;
	try { data = await res.json(); }
	catch { throw new Error("Sync failed: invalid export JSON"); }
	if (!isRecord(data) || typeof data.version !== "string" || !data.version ||
		typeof data.exported_at !== "string" || !Number.isFinite(Date.parse(data.exported_at)) ||
		!isExportList(data, "sessions") || !isExportList(data, "observations") ||
		!isExportList(data, "prompts") ||
		(data.relations !== undefined && !isExportList(data, "relations"))) {
		throw new Error("Sync failed: incompatible export payload");
	}
	const sessions = (data.sessions ?? []) as unknown[];
	const observations = (data.observations ?? []) as unknown[];
	if (!sessions.every(isSession) || !observations.every(isObservation)) {
		throw new Error("Sync failed: incompatible export payload");
	}
	const owners = new Map((sessions as ExportSession[]).map(session => [session.id, session.project.toLowerCase()]));
	if (project && observations.some(value => {
		const obs = value as ExportObservation;
		return (obs.project?.trim() || owners.get(obs.session_id || "") || "").toLowerCase() !== project.toLowerCase();
	})) {
		throw new Error("Sync failed: incompatible export payload (foreign project)");
	}
	if (new Set((observations as ExportObservation[]).map(obs => obs.id)).size !== observations.length) {
		throw new Error("Sync failed: incompatible export payload (duplicate observation id)");
	}
	return { version: data.version, exported_at: data.exported_at,
		sessions: sessions as ExportSession[], observations: observations as ExportObservation[],
		prompts: (data.prompts ?? []) as unknown[], relations: (data.relations ?? []) as unknown[] };
}

function isExportList(data: Record<string, unknown>, field: string): boolean {
	return Object.prototype.hasOwnProperty.call(data, field) &&
		(data[field] === null || Array.isArray(data[field]));
}

function isSession(value: unknown): value is ExportSession {
	return isRecord(value) && typeof value.id === "string" && value.id.length > 0 &&
		typeof value.project === "string";
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isObservation(value: unknown): value is ExportObservation {
	if (!isRecord(value)) return false;
	return Number.isSafeInteger(value.id) && (value.id as number) > 0 &&
		typeof value.sync_id === "string" && value.sync_id.length > 0 &&
		!/[\x00-\x1f]/.test(value.sync_id) &&
		typeof value.title === "string" && typeof value.content === "string" &&
		typeof value.type === "string" && value.type.length > 0 &&
		(value.project === undefined || value.project === null || typeof value.project === "string") &&
		typeof value.created_at === "string" && Number.isFinite(Date.parse(value.created_at)) &&
		typeof value.updated_at === "string" && Number.isFinite(Date.parse(value.updated_at));
}

async function readState(vault: Vault, path: string): Promise<SyncState> {
	const empty: SyncState = { lastSyncAt: "", files: {}, version: STATE_VERSION };
	try {
		if (!await vault.adapter.exists(path)) return empty;
		const parsed = JSON.parse(await vault.adapter.read(path));
		return {
			lastSyncAt: typeof parsed.lastSyncAt === "string" ? parsed.lastSyncAt : "",
			files: isRecord(parsed.files) ? parsed.files as Record<string, string> : {},
			version: STATE_VERSION,
		};
	} catch { return empty; }
}

async function writeState(vault: Vault, path: string, state: SyncState): Promise<void> {
	const content = JSON.stringify(state, null, 2);
	const file = vault.getFileByPath(path);
	if (file) await vault.modify(file, content);
	else await vault.create(path, content);
}

async function ensureFolder(vault: Vault, path: string): Promise<void> {
	if (!await vault.adapter.exists(path)) await vault.createFolder(path);
}

async function hashContent(content: string): Promise<string> {
	const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(content));
	return Array.from(new Uint8Array(hash)).map(b => b.toString(16).padStart(2, "0")).join("");
}
