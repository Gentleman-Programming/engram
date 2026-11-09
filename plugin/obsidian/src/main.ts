import { Notice, Plugin } from "obsidian";
import {
	EngramSettings,
	EngramSettingTab,
	DEFAULT_SETTINGS,
	formatRelative,
} from "./settings";
import { syncNow, SyncResult } from "./sync";

// ─── Plugin ───────────────────────────────────────────────────────────────────

export default class EngramBrainPlugin extends Plugin {
	settings!: EngramSettings;

	/** Numeric interval handle returned by window.setInterval. Null when auto-sync is off. */
	private _syncInterval: number | null = null;

	/** Status bar element — updated after every sync attempt. */
	private statusBarItem: HTMLElement | null = null;

	/** Last successful note count — preserved across failed syncs (REQ-PLUGIN-05). */
	private lastSuccessCount: number = 0;

	// ─── Lifecycle ────────────────────────────────────────────────────────────

	async onload() {
		await this.loadSettings();

		// Preserve the last successful count from persisted settings
		this.lastSuccessCount = this.settings.lastSyncCount;

		// Settings tab (REQ-PLUGIN-02)
		this.addSettingTab(new EngramSettingTab(this.app, this));

		// Ribbon button (REQ-PLUGIN-03)
		const ribbonEl = this.addRibbonIcon(
			"brain",
			"Sync Engram Brain",
			async () => {
				await this.syncNow();
			}
		);
		ribbonEl.addClass("engram-brain-ribbon");

		// Command palette entry (bonus usability)
		this.addCommand({
			id: "sync-engram",
			name: "Sync Engram Brain",
			callback: () => {
				this.syncNow();
			},
		});

		// Status bar (REQ-PLUGIN-05)
		this.statusBarItem = this.addStatusBarItem();
		this.statusBarItem.setText("Engram: ready");

		// Optional auto-sync polling (REQ-PLUGIN-01)
		if (this.settings.autoSyncMinutes > 0) {
			this.startAutoSync();
		}
	}

	async onunload() {
		// Clear polling interval so no further HTTP calls are made (REQ-PLUGIN-01)
		this.stopAutoSync();
	}

	// ─── Settings ─────────────────────────────────────────────────────────────

	async loadSettings() {
		const data = await this.loadData();
		this.settings = Object.assign({}, DEFAULT_SETTINGS, data);
	}

	async saveSettings() {
		await this.saveData(this.settings);
	}

	// ─── Auto-sync ────────────────────────────────────────────────────────────

	/**
	 * Start the auto-sync polling interval.
	 * If one is already running it is cleared first.
	 */
	startAutoSync() {
		this.stopAutoSync();
		const minutes = this.settings.autoSyncMinutes;
		if (minutes <= 0) return;

		this._syncInterval = window.setInterval(
			() => {
				this.syncNow();
			},
			minutes * 60 * 1000
		);
	}

	/** Clear any active auto-sync interval. Safe to call when none is active. */
	stopAutoSync() {
		if (this._syncInterval !== null) {
			window.clearInterval(this._syncInterval);
			this._syncInterval = null;
		}
	}

	/**
	 * Restart (or clear) the auto-sync interval based on current settings.
	 * Called from the settings tab whenever the interval changes (REQ-PLUGIN-02).
	 */
	restartAutoSync() {
		this.stopAutoSync();
		if (this.settings.autoSyncMinutes > 0) {
			this.startAutoSync();
		}
	}

	// ─── Sync ─────────────────────────────────────────────────────────────────

	/**
	 * Trigger a manual or automatic sync.
	 *
	 * - Updates the status bar before + after (REQ-PLUGIN-05)
	 * - Shows a Notice on success or failure (REQ-PLUGIN-03)
	 * - Persists lastSyncAt + lastSyncCount on success
	 */
	async syncNow(): Promise<void> {
		this.setStatusBar("Engram: syncing…");

		let result: SyncResult;
		try {
			result = await syncNow(this);
		} catch (err) {
			// ── Failure path ────────────────────────────────────────────────
			const msg =
				err instanceof Error
					? err.message
					: "Sync failed: unknown error";
			new Notice(msg);
			// REQ-PLUGIN-05: on failure, preserve previous count
			this.setStatusBarFailure();
			return;
		}

		// ── Success path ──────────────────────────────────────────────────────
		const total = result.created + result.updated;
		const skipped = result.skipped;
		const deleted = result.deleted;

		const parts: string[] = [];
		if (total > 0) parts.push(`${total} notes written`);
		if (deleted > 0) parts.push(`${deleted} deleted`);
		if (skipped > 0) parts.push(`${skipped} unchanged`);

		const summary = parts.length > 0 ? parts.join(", ") : "nothing changed";
		new Notice(`Engram: ${summary}`);

		// Persist sync metadata
		const now = new Date().toISOString();
		this.settings.lastSyncAt = now;
		this.settings.lastSyncCount = result.total;
		this.lastSuccessCount = result.total;
		await this.saveSettings();

		// REQ-PLUGIN-05: success status bar
		this.setStatusBarSuccess(this.lastSuccessCount);
	}

	// ─── Status Bar ───────────────────────────────────────────────────────────

	/**
	 * Set status bar to an arbitrary text message.
	 */
	private setStatusBar(text: string) {
		if (this.statusBarItem) {
			this.statusBarItem.setText(text);
		}
	}

	/**
	 * REQ-PLUGIN-05 — Success: "Engram: N notes · synced just now"
	 *
	 * Uses `lastSyncCount` from settings so that re-opens after restart
	 * show the previously synced count until a new sync completes.
	 */
	private setStatusBarSuccess(count: number) {
		const timeStr = this.settings.lastSyncAt
			? formatRelative(new Date(this.settings.lastSyncAt))
			: "just now";
		this.setStatusBar(`Engram: ${count} notes · synced ${timeStr}`);
	}

	/**
	 * REQ-PLUGIN-05 — Failure: "Engram: sync failed · {relative time}"
	 *
	 * Does NOT overwrite `lastSuccessCount` — preserved from the last
	 * successful sync so the count shown in settings is still accurate.
	 */
	private setStatusBarFailure() {
		const timeStr = formatRelative(new Date());
		this.setStatusBar(`Engram: sync failed · ${timeStr}`);
	}
}
