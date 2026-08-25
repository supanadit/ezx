// api.js — operator-facing API routes + Patroni role-change pgBackRest
// reconfiguration, consolidated from routes.js + patroni.js + backup.js's route
// registration. All handlers live on the shared health server (EZX_HEALTH_ADDR)
// and run on HTTP goroutines while the chain supervises. The Patroni role check
// runs as a scheduler.every JS callback (replacing the old scheduled curl child
// + HTTP route) so a promote/demote regenerates pgbackrest.conf without any
// external callback executable.
const { env, fs, editor, api, process, probe, shell, scheduler, log } = require("ezx");
const {
	PGDATA,
	PG_USER,
	PG_GROUP,
	PG_PORT,
	PG_DB,
	PATRONI_ENABLE,
	PGBOUNCER_ENABLE,
	PGBACKREST_ENABLE,
	PGBACKREST_CONF,
	STANZA,
	PGBACKREST_AUTO,
	CITUS_ENABLE,
	CITUS_ROLE,
} = require("./env");
const { backupStandby } = require("./config");

let lastRole = null;

// isPrimaryRole — true when this node is the cluster primary.
// Patroni: REST /master or /leader returns 2xx. Citus: coordinator only.
// Native: pg_is_in_recovery() returns "f".
function isPrimaryRole() {
	if (PATRONI_ENABLE) {
		const base = env.get("PATRONI_REST_URL", "http://localhost:8008");
		if (probe.http(base + "/master", 200)) return true;
		if (probe.http(base + "/leader", 200)) return true;
		return false;
	}
	if (CITUS_ENABLE) {
		return CITUS_ROLE === "coordinator";
	}
	if (env.bool("HA_MODE_NATIVE_OR_FORCE_PRIMARY")) return true;
	// Fallback: check pg_is_in_recovery via psql (args array, no shell).
	const res = process.capture({
		process: {
			binaryPath: "psql",
			arguments: [
				"-h",
				env.get("POSTGRES_HOST", "127.0.0.1"),
				"-p",
				PG_PORT,
				"-U",
				PG_USER,
				"-d",
				env.get("POSTGRES_DB", PG_DB),
				"-tA",
				"-c",
				"select pg_is_in_recovery();",
			],
			environment: ["PGPASSWORD=" + (env.get("POSTGRES_PASSWORD") || "")],
			check: false,
		},
	});
	return res.code === 0 && res.stdout.includes("f");
}

function rewriteConfig(removePg2, standbyMode) {
	if (!fs.exists(PGBACKREST_CONF)) return;
	const e = editor.open(PGBACKREST_CONF);
	const content =
		e
			.read()
			.split("\n")
			.filter((l) => !/^pg2-/.test(l) && !/^backup-standby=/.test(l))
			.join("\n")
			.replace(/\n+$/, "") + "\n";
	e.replace(content + (standbyMode ? "backup-standby=" + standbyMode + "\n" : ""));
	fs.chmod(PGBACKREST_CONF, 0o640);
	fs.chown(PGBACKREST_CONF, PG_USER + ":" + PG_GROUP);
}

function regenerateForPrimary() {
	log.info("[patroni] role change: primary — removing pg2-host settings");
	rewriteConfig(true, "n");
}

function regenerateForReplica() {
	const primaryPath = env.get("PGBACKREST_PRIMARY_PATH");
	const primaryHost = env.get("PGBACKREST_PRIMARY_HOST", env.get("PRIMARY_HOST"));
	if (!primaryPath || !primaryHost) {
		log.warn("[patroni] replica role but no PGBACKREST_PRIMARY_PATH/HOST; leaving pgbackrest.conf");
		return;
	}
	const standbyMode = backupStandby(env.get("PGBACKREST_BACKUP_STANDBY")) || "y";
	const primaryPort = env.get("PGBACKREST_PRIMARY_PORT", env.get("PRIMARY_PORT", "5432"));
	const sshPort = env.get("PGBACKREST_PRIMARY_SSH_PORT", "22");
	const sshUser = env.get("PGBACKREST_PRIMARY_SSH_USER", "postgres");
	const sshKey = env.get("PGBACKREST_PRIMARY_SSH_KEY_FILE", "/home/postgres/.ssh/id_rsa");

	log.info("[patroni] role change: replica — adding pg2-host for standby backup");
	rewriteConfig(false, standbyMode);
	editor
		.open(PGBACKREST_CONF)
		.append(
			"pg2-host=" + primaryHost + "\n" +
				"pg2-host-port=" + sshPort + "\n" +
				"pg2-host-user=" + sshUser + "\n" +
				"pg2-port=" + primaryPort + "\n" +
				"pg2-user=" + env.get("PGBACKREST_PRIMARY_USER", PG_USER) + "\n" +
				"pg2-host-key-file=" + sshKey + "\n",
		);
}

// checkRole — poll the current role and regenerate pgbackrest.conf on change.
function checkRole() {
	if (!isPrimaryRole()) {
		if (lastRole !== "replica") {
			regenerateForReplica();
			lastRole = "replica";
			return { role: "replica", changed: true };
		}
		return { role: "replica", changed: false };
	}
	if (lastRole !== "primary") {
		regenerateForPrimary();
		lastRole = "primary";
		return { role: "primary", changed: true };
	}
	return { role: "primary", changed: false };
}

// handleSyncReload — rewrite synchronous_standby_names and reload without
// restart (port of misc/pg-reload-sync-config.sh). POST body/query:
//   mode=true&count=1&replicas=pg2  |  mode=false
function handleSyncReload() {
	const mode = env.get("SYNC_MODE", env.get("REPLICATION_SYNCHRONOUS_MODE", "true"));
	const count = env.get("SYNC_COUNT", env.get("REPLICATION_SYNCHRONOUS_COUNT", ""));
	const replicas = env.get("SYNC_REPLICAS", env.get("REPLICATION_SYNCHRONOUS_REPLICAS", ""));

	const conf = PGDATA + "/postgresql.conf";
	if (!fs.exists(conf)) throw new Error("postgresql.conf not found at " + conf);

	const e = editor.open(conf);
	if (mode === "true") {
		const ssn = count && replicas ? "ANY " + count + " (" + replicas + ")" : "*";
		e.upsert("^\\s*synchronous_standby_names\\s*=", "synchronous_standby_names = '" + ssn + "'");
	} else {
		e.remove("^\\s*synchronous_standby_names\\s*=");
	}

	const code = process.run({
		process: {
			binaryPath: "pg_ctl",
			arguments: ["-D", PGDATA, "reload"],
			user: PG_USER,
			group: PG_GROUP,
		},
	});
	if (code !== 0) throw new Error("pg_ctl reload failed (code=" + code + ")");
	return { ok: true, synchronous_standby_names: mode === "true" ? (count && replicas ? "ANY " + count + " (" + replicas + ")" : "*") : "(removed)" };
}

// healthCheck — composite health probes (port of runtime/healthcheck.sh).
function checkPostgresql() {
	const host = env.get("POSTGRES_HOST", "127.0.0.1");
	return probe.tcp(host, parseInt(PG_PORT, 10)) ||
		probe.exec("pg_isready", "-h", host, "-p", PG_PORT, "-U", PG_USER, "-d", env.get("POSTGRES_DB", PG_DB));
}

function checkPatroni() {
	if (!PATRONI_ENABLE) return true;
	return probe.http(env.get("PATRONI_REST_URL", "http://localhost:8008") + "/patroni", 200);
}

function checkPgbouncer() {
	if (!PGBOUNCER_ENABLE) return true;
	const port = env.int("PGBOUNCER_LISTEN_PORT", 6432);
	return probe.process("pgbouncer") && probe.tcp("127.0.0.1", port);
}

function checkDisk() {
	return probe.disk(PGDATA, 10, 100);
}

function checkProcess() {
	return probe.process("postgres") && probe.zombies() === 0;
}

function handleHealth() {
	const checks = {
		postgresql: checkPostgresql(),
		patroni: checkPatroni(),
		pgbouncer: checkPgbouncer(),
		disk: checkDisk(),
		process: checkProcess(),
	};
	// Only the main program determines health. Sidecars are optional.
	const main = PATRONI_ENABLE ? checks.patroni : checks.postgresql;
	return { healthy: main, checks };
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

// registerRoleCheck — build the in-process Patroni role-check scheduled node
// (the approved deviation: replaces the old curl child + HTTP route). Returns
// a ProcessNode whose scheduler runs the JS callback on a cron tick; the
// orchestrator invokes the callback instead of spawning a process. Must be
// attached to the chain (runtime.js adds it to children). gated on
// PATRONI_ENABLE && PGBACKREST_ENABLE && EZX_HEALTH_ADDR.
function roleCheckNode() {
	if (!PATRONI_ENABLE || !PGBACKREST_ENABLE) return null;
	if (!env.get("EZX_HEALTH_ADDR")) return null;
	return {
		name: "patroni-role-check",
		optional: true,
		process: { binaryPath: "/bin/true" }, // never spawned; Tick runs instead
		scheduler: scheduler.every(env.get("PATRONI_ROLE_CHECK_CRON", "*/1 * * * *"), () => checkRole(), {
			timezone: env.get("PGBACKREST_AUTO_TIMEZONE", "UTC"),
			initialDelay: 30e9,
		}),
	};
}

// registerOpsRoutes — bind operator routes + backup routes; no-op without
// EZX_HEALTH_ADDR. The role-check node is added to the chain by runtime.js.
function registerOpsRoutes() {
	if (!env.get("EZX_HEALTH_ADDR")) return;
	api.post("/sync-reload", () => handleSyncReload());
	api.get("/health/comprehensive", () => handleHealth());
	registerBackupRoutes();
}

module.exports = {
	registerOpsRoutes,
	registerBackupRoutes,
	roleCheckNode,
	handleSyncReload,
	handleHealth,
	checkRole,
	regenerateForPrimary,
	regenerateForReplica,
	isPrimaryRole,
};
