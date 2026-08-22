// backup.js — runtime/backup: scheduled pgBackRest backups driven by the ezx
// cron scheduler, plus user-defined manual-trigger API routes. Replaces the
// bash runtime/backup-scheduler.sh entirely — no shell involved.
const { env, scheduler, api } = require("ezx");
const {
	PG_USER,
	PG_GROUP,
	PG_PORT,
	STANZA,
	PATRONI_ENABLE,
	PGBACKREST_ENABLE,
	PGBACKREST_AUTO,
} = require("./env");

// Backup flags from env, mirroring the shell scheduler's run_backup (no bash).
function backupFlags(type) {
	const args = [
		"--config=/etc/pgbackrest.conf",
		"--stanza=" + STANZA,
		"backup",
		"--type=" + type,
	];
	const flag = (envName, arg) => {
		if (env.isTruthy(envName)) args.push(arg);
	};
	const flagDefault = (envName, arg, def) => {
		if (env.isTruthy(envName, def)) args.push(arg);
	};
	flag("PGBACKREST_BACKUP_START_FAST", "--start-fast");
	flag("PGBACKREST_BACKUP_STOP_AUTO", "--stop-auto");
	flagDefault("PGBACKREST_BACKUP_VERIFY", "--verify", "true");
	flag("PGBACKREST_BACKUP_CHECKSUM_PAGE", "--checksum-page");
	flagDefault("PGBACKREST_BACKUP_ARCHIVE_CHECK", "--archive-check", "true");
	flag("PGBACKREST_BACKUP_ARCHIVE_COPY", "--archive-copy");
	flag("PGBACKREST_BACKUP_EXPIRE_AUTO", "--expire-auto");
	flagDefault("PGBACKREST_BACKUP_RESUME", "--resume", "true");
	flagDefault(
		"PGBACKREST_BACKUP_ARCHIVE_MISSING_RETRY",
		"--archive-missing-retry",
		"true",
	);
	for (const x of (env.get("PGBACKREST_BACKUP_EXCLUDE") || "")
		.split(/[, ]+/)
		.filter(Boolean))
		args.push("--exclude=" + x);
	for (const a of (env.get("PGBACKREST_BACKUP_ANNOTATION") || "")
		.split(/[, ]+/)
		.filter(Boolean))
		args.push("--annotation=" + a);
	return args;
}

// Primary-role gate (postgres-specific, composed from generic probes):
// Patroni REST /master → 2xx when primary; Citus role env; else
// pg_is_in_recovery()=f via psql. The scheduled node skips ticks while the
// gate fails, so replicas never run backups.
function primaryGate() {
	if (PATRONI_ENABLE) {
		return {
			type: "http",
			http: {
				url: env.get("PATRONI_REST_URL", "http://localhost:8008") + "/master",
			},
		};
	}
	if (env.isTruthy("CITUS_ENABLE")) {
		return env.get("CITUS_ROLE") === "coordinator"
			? { type: "exec", exec: ["true"] }
			: { type: "exec", exec: ["false"] };
	}
	return {
		type: "exec",
		exec: [
			"psql",
			"-qtAX",
			"-U",
			PG_USER,
			"-h",
			"127.0.0.1",
			"-p",
			PG_PORT,
			"-d",
			"postgres",
			"-c",
			"select pg_is_in_recovery();",
		],
		execExpect: "f",
	};
}

// A scheduled pgBackRest backup node: ezx runs the node's Process on the cron
// tick, gated on primary-role, and skips duplicate ticks within a minute.
function backupNode(type, defaultCron) {
	return {
		name: "backup-" + type,
		needParentReady: true,
		process: {
			binaryPath: "pgbackrest",
			arguments: backupFlags(type),
			user: PG_USER,
			group: PG_GROUP,
			// Replace the shell's generate_clean_env_command (env -u PGBACKREST_*).
			filterEnvPattern: ["^PGBACKREST_"],
		},
		scheduler: scheduler.build({
			schedule: {
				expression: env.get(
					"PGBACKREST_AUTO_" + type.toUpperCase() + "_CRON",
					defaultCron,
				),
				timezone: env.get("PGBACKREST_AUTO_TIMEZONE", "UTC"),
			},
			initialDelay: 120e9, // PGBACKREST_AUTO_FIRST_INCR_DELAY, in ns
			minInterval: 60e9, // one backup per minute, matching the shell dedup
			gate: primaryGate(),
		}),
	};
}

// Scheduled backup children attached to the postgres node (needParentReady so
// backups start only after the DB is up, replacing PGBACKREST_PARENT_PID).
function backupNodes() {
	if (!PGBACKREST_ENABLE || !PGBACKREST_AUTO) return [];
	return [
		backupNode("full", "0 2 * * *"),
		backupNode("diff", "20 2,8,14,20 * * *"),
		backupNode("incr", "*/15 * * * *"),
	];
}

// User-defined manual-trigger routes on the shared EZX_HEALTH_ADDR server.
// scheduler.trigger(name) fires a scheduled node immediately (fire-and-forget).
function registerBackupRoutes() {
	if (!env.get("EZX_HEALTH_ADDR")) return;
	for (const type of ["full", "diff", "incr"]) {
		api.post("/backup/" + type, () => {
			const fired = scheduler.trigger("backup-" + type);
			return {
				ok: fired,
				type: type,
				inflight: scheduler.status("backup-" + type).inflight,
			};
		});
	}
	api.get("/backup/status", () => ({
		full: scheduler.status("backup-full"),
		diff: scheduler.status("backup-diff"),
		incr: scheduler.status("backup-incr"),
	}));
}

module.exports = {
	backupFlags,
	primaryGate,
	backupNode,
	backupNodes,
	registerBackupRoutes,
};