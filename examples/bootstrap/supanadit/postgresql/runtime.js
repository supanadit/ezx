// runtime.js — runtime/startup: supervise the database + sidecar process tree.
// ezx stays PID 1, forwards signals, reaps zombies, and drains on SIGTERM.
const { env, fs, process, chain } = require("ezx");
const {
	PGDATA,
	PG_USER,
	PG_GROUP,
	PG_PORT,
	PATRONI_ENABLE,
	PGBOUNCER_ENABLE,
	PATRONI_CONF,
	PGBOUNCER_INI,
	REPL_ROLE,
} = require("./env");
const { postgresFiles } = require("./config");
const { backupNodes, registerBackupRoutes } = require("./backup");

function isPrimary() {
	return !(REPL_ROLE === "replica");
}

function startPgbouncer() {
	if (!PGBOUNCER_ENABLE) return null;
	if (!fs.exists(PGBOUNCER_INI))
		throw new Error("pgbouncer.ini not found: " + PGBOUNCER_INI);
	const p = process.spawn({
		name: "pgbouncer",
		process: {
			binaryPath: "pgbouncer",
			arguments: ["-d", PGBOUNCER_INI],
			user: PG_USER,
			group: PG_GROUP,
		},
	});
	p.start([]);
	return p;
}

function startSshd() {
	if (!isPrimary() || !fs.exists("/usr/sbin/sshd")) return null;
	const p = process.spawn({
		name: "sshd",
		process: { binaryPath: "/usr/sbin/sshd" },
	});
	p.start([]);
	return p;
}

function startSleep() {
	chain.run({
		roots: [
			{
				name: "sleep",
				process: {
					binaryPath: "/bin/sh",
					arguments: ["-c", "while true; do sleep 3600; done"],
				},
				restart: { mode: "always", backoff: 1000 * 1e6 },
			},
		],
	});
}

function startDatabase() {
	const mainProcess = PATRONI_ENABLE
		? {
				binaryPath: "patroni",
				arguments: [PATRONI_CONF],
				user: PG_USER,
				group: PG_GROUP,
			}
		: {
				binaryPath: env.get("POSTGRES_BIN", "postgres"),
				arguments: ["-D", PGDATA],
				user: PG_USER,
				group: PG_GROUP,
			};

	const node = {
		name: PATRONI_ENABLE ? "patroni" : "postgres",
		process: mainProcess,
		files: postgresFiles,
		forwardSignals: ["SIGTERM", "SIGINT", "SIGHUP", "SIGUSR1", "SIGUSR2"],
		shutdown: { timeout: 30 * 1e9, forceKill: true },
		// Scheduled pgBackRest backups run as supervised children of the database,
		// gated on primary-role and triggerable manually via /backup/*.
		children: backupNodes(),
	};

	// When the health server is enabled, gate /readyz on the DB port and expose
	// user-defined manual-trigger routes. ezx stays PID 1 and supervises all
	// children, forwarding signals and reaping — it never exec's away.
	const healthAddr = env.get("EZX_HEALTH_ADDR");
	if (healthAddr) {
		node.health = {
			readyProbe: {
				type: "tcp",
				tcp: { host: "127.0.0.1", port: parseInt(PG_PORT, 10) },
			},
		};
		registerBackupRoutes();
	}

	chain.run({ roots: [node] });
}

module.exports = {
	isPrimary,
	startPgbouncer,
	startSshd,
	startSleep,
	startDatabase,
};