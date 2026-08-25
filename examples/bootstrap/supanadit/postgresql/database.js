// database.js — init/02-database + runtime restore: initdb, superuser password,
// replication user, HA clone, cluster verification, and pgBackRest restore.
// Mirrors bash init/02-database.sh and startup.sh:perform_pgbackrest_restore.
const { env, fs, editor, process, shell, log } = require("ezx");
const {
	PGDATA,
	PGRUN,
	PG_USER,
	PG_GROUP,
	PG_PORT,
	PG_PASS,
	STANZA,
	PATRONI_ENABLE,
	PGBACKREST_ENABLE,
	HA_MODE,
	REPL_ROLE,
	REPL_USER,
	REPL_PASSWORD,
	PRIMARY_HOST,
	PRIMARY_PORT,
	RESTORE_SENTINEL,
	RESTORE_COMPLETE_MARK,
} = require("./env");

const TIMEOUT_CHANGE_PASSWORD = env.int("TIMEOUT_CHANGE_PASSWORD", 5);

// Run a one-shot command as the postgres user with an args array (no shell).
function runPg(argv, name) {
	return process.run({
		name: name || "pg",
		process: {
			binaryPath: argv[0],
			arguments: argv.slice(1),
			user: PG_USER,
			group: PG_GROUP,
		},
	});
}

function clusterExists() {
	return ["PG_VERSION", "postgresql.conf", "pg_hba.conf"].some((f) =>
		fs.exists(PGDATA + "/" + f),
	);
}

function isRestoreRequested() {
	return env.bool("PGBACKREST_RESTORE");
}

function cleanupStaleRestoreMarkers() {
	fs.ensureDir(PGRUN, { mode: 0o755 });
	if (fs.exists(RESTORE_SENTINEL)) fs.remove(RESTORE_SENTINEL);
}

function prepareRestoreEnvironment() {
	const owner = PG_USER + ":" + PG_GROUP;
	if (fs.exists(PGDATA) && fs.readDir(PGDATA).length > 0) {
		const backupPath = PGDATA + ".pre-restore." + Math.floor(Date.now() / 1000);
		log.warn("Data directory not empty; moving existing contents to " + backupPath);
		try {
			fs.rename(PGDATA, backupPath);
			fs.chownRecursive(backupPath, owner);
		} catch {
			log.warn("Standard move failed; attempting copy-and-clean fallback");
			fs.ensureDir(backupPath, { mode: 0o700 });
			// copy fallback via shell (fs exposes no recursive copy)
			if (
				process.run({
					process: {
						binaryPath: "/bin/sh",
						arguments: ["-c", "cp -a " + shell.quote(PGDATA) + "/. " + shell.quote(backupPath) + "/"],
						user: PG_USER,
						group: PG_GROUP,
					},
				}) !== 0
			) {
				throw new Error("Failed to safeguard existing data directory contents");
			}
			fs.chownRecursive(backupPath, owner);
			fs.removeAll(PGDATA);
		}
	}
	fs.ensureDir(PGDATA, { mode: 0o700 });
	fs.chown(PGDATA, owner);
	fs.chmod(PGDATA, 0o700);
}

function markRestorePending() {
	fs.ensureDir(PGRUN, { mode: 0o755 });
	fs.write(RESTORE_SENTINEL, "requested_at=" + new Date().toISOString() + "\n", { mode: 0o600 });
}

function configureReplicaAppname() {
	const autoConf = PGDATA + "/postgresql.auto.conf";
	if (!fs.exists(autoConf)) return;
	const appname = env.get("REPLICATION_APPNAME", require("os").hostname());
	editor
		.open(autoConf)
		.upsert(
			"^\\s*primary_conninfo",
			"primary_conninfo = 'application_name=" + appname + "'",
		);
}

function clonePrimary() {
	if (!PRIMARY_HOST) throw new Error("PRIMARY_HOST required for replica");
	if (fs.readDir(PGDATA).length > 0)
		throw new Error("Data directory is not empty. Cannot clone primary.");

	const argv = [
		"pg_basebackup",
		"-h",
		PRIMARY_HOST,
		"-p",
		PRIMARY_PORT,
		"-U",
		REPL_USER || "replicator",
		"-D",
		PGDATA,
		"-Fp",
		"-Xs",
		"-R",
	];
	if (
		process.run({
			process: {
				binaryPath: argv[0],
				arguments: argv.slice(1),
				env: ["PGPASSWORD=" + REPL_PASSWORD],
				user: PG_USER,
				group: PG_GROUP,
			},
		}) !== 0
	) {
		fs.removeAll(PGDATA);
		throw new Error("pg_basebackup failed");
	}
	configureReplicaAppname();
}

// Temp-server helper: write a minimal config, start postgres on port 5433,
// run the SQL as postgres, stop, then secure-clean the config.
function runOnTempServer(sql, name) {
	const cfg = fs.tempFile("/tmp", "pg_tmp");
	fs.write(
		cfg,
		"listen_addresses = 'localhost'\nport = 5433\nunix_socket_directories = '" +
			PGDATA +
			"'\npassword_encryption = scram-sha-256\n",
		{ mode: 0o644 },
	);
	try {
		if (
			process.run({
				process: {
					binaryPath: "pg_ctl",
					arguments: ["-D", PGDATA, "-o", "--config-file=" + cfg, "-s", "start"],
					user: PG_USER,
					group: PG_GROUP,
				},
			}) !== 0
		) {
			throw new Error("Failed to start postgres for " + name);
		}
		process.run({ process: { binaryPath: "sleep", arguments: ["2"], user: PG_USER, group: PG_GROUP } });
		const res = process.run({
			process: {
				binaryPath: "timeout",
				arguments: [String(TIMEOUT_CHANGE_PASSWORD), "psql", "-h", "localhost", "-p", "5433", "-U", "postgres", "-d", "postgres", "-c", sql],
				user: PG_USER,
				group: PG_GROUP,
			},
		});
		process.run({ process: { binaryPath: "pg_ctl", arguments: ["-D", PGDATA, "-s", "stop"], user: PG_USER, group: PG_GROUP } });
		if (res !== 0) throw new Error("Failed to run " + name + " SQL");
	} finally {
		// Note: no secure shred in ezx 0.0.1; fs.remove leaves freed blocks.
		fs.remove(cfg);
	}
}

function setPassword() {
	if (!PG_PASS) return;
	runOnTempServer("ALTER USER postgres PASSWORD '" + String(PG_PASS).replace(/'/g, "''") + "';", "pw");
}

function createReplicationUser() {
	if (HA_MODE !== "native" || REPL_ROLE !== "primary") return;
	runOnTempServer(
		"CREATE USER " +
			(REPL_USER || "replicator") +
			" REPLICATION LOGIN PASSWORD '" +
			String(REPL_PASSWORD || "replicator_password").replace(/'/g, "''") +
			"';",
		"repl-user",
	);
}

function verifyClusterIntegrity() {
	for (const f of ["PG_VERSION", "postgresql.conf", "pg_hba.conf", "pg_ident.conf"]) {
		if (!fs.exists(PGDATA + "/" + f)) throw new Error("Required cluster file missing: " + f);
	}
	for (const d of ["base", "global", "pg_xact", "pg_wal"]) {
		if (!fs.exists(PGDATA + "/" + d)) throw new Error("Required cluster directory missing: " + d);
	}
	const cfg = fs.tempFile("/tmp", "pg_test");
	fs.write(
		cfg,
		"listen_addresses = ''\nport = 5433\nunix_socket_directories = '" + PGDATA + "'\n",
		{ mode: 0o644 },
	);
	try {
		if (
			process.run({
				process: {
					binaryPath: "pg_ctl",
					arguments: ["-D", PGDATA, "-o", "--config-file=" + cfg, "-s", "start"],
					user: PG_USER,
					group: PG_GROUP,
				},
			}) !== 0
		) {
			throw new Error("Failed to start cluster for testing");
		}
		process.run({ process: { binaryPath: "sleep", arguments: ["2"], user: PG_USER, group: PG_GROUP } });
		const status = process.run({ process: { binaryPath: "pg_ctl", arguments: ["-D", PGDATA, "status"], user: PG_USER, group: PG_GROUP } });
		process.run({ process: { binaryPath: "pg_ctl", arguments: ["-D", PGDATA, "-s", "stop"], user: PG_USER, group: PG_GROUP } });
		if (status !== 0) throw new Error("Cluster failed to start properly");
	} finally {
		fs.remove(cfg);
	}
}

function initdb() {
	// Clear stale restore markers before any logic (bash 02-database.sh:39).
	cleanupStaleRestoreMarkers();

	if (isRestoreRequested()) {
		if (!PGBACKREST_ENABLE)
			throw new Error("PGBACKREST_RESTORE=true requires PGBACKREST_ENABLE=true");
		prepareRestoreEnvironment();
		markRestorePending();
		log.info("Restore requested; waiting for runtime restore");
		return;
	}

	if (clusterExists()) {
		log.info("Cluster exists, skipping initdb");
		return;
	}
	if (PATRONI_ENABLE) return;
	if (HA_MODE === "native" && REPL_ROLE === "replica") {
		clonePrimary();
		return;
	}

	const argv = [
		"initdb",
		"--auth=trust",
		"--username=postgres",
		"--encoding=UTF-8",
		"--locale=C",
	];
	const initdbArgs = env.get("POSTGRES_INITDB_ARGS");
	if (initdbArgs) argv.push(...initdbArgs.split(/\s+/));
	const waldir = env.get("POSTGRES_INITDB_WALDIR");
	if (waldir) argv.push("--waldir=" + waldir);
	argv.push("--pgdata=" + PGDATA);
	if (process.run({ process: { binaryPath: argv[0], arguments: argv.slice(1), user: PG_USER, group: PG_GROUP } }) !== 0)
		throw new Error("initdb failed");
	fs.chmod(PGDATA, 0o700);
	if (fs.exists(PGDATA + "/PG_VERSION")) fs.chmod(PGDATA + "/PG_VERSION", 0o644);

	setPassword();
	createReplicationUser();
	verifyClusterIntegrity();
}

// Build pgBackRest restore args from env (full parity with
// start/perform_pgbackrest_restore).
function restoreArgs() {
	const args = ["--config=/etc/pgbackrest.conf", "--stanza=" + STANZA, "restore"];
	const opt = (v, f) => {
		if (v) args.push("--" + f + "=" + v);
	};
	const optList = (v, f) => {
		if (!v) return;
		for (const item of v.split(",").filter(Boolean)) args.push("--" + f + "=" + item.trim());
	};
	const flag = (v, f) => {
		if (env.bool(v)) args.push("--" + f);
	};
	opt(env.get("PGBACKREST_RESTORE_TYPE"), "type");
	opt(env.get("PGBACKREST_RESTORE_TARGET"), "target");
	opt(env.get("PGBACKREST_RESTORE_TARGET_TIMELINE"), "target-timeline");
	opt(env.get("PGBACKREST_RESTORE_TARGET_ACTION"), "target-action");
	opt(env.get("PGBACKREST_RESTORE_TARGET_XID"), "target-xid");
	opt(env.get("PGBACKREST_RESTORE_TARGET_LSN"), "target-lsn");
	opt(env.get("PGBACKREST_RESTORE_TARGET_NAME"), "target-name");
	flag("PGBACKREST_RESTORE_TARGET_IMMEDIATE", "target-immediate");
	opt(env.get("PGBACKREST_RESTORE_RECOVERY_TARGET_TLI"), "recovery-target-tli");
	opt(env.get("PGBACKREST_RESTORE_RECOVERY_TARGET_ACTION"), "recovery-target-action");
	opt(env.get("PGBACKREST_RESTORE_SET"), "set");
	optList(env.get("PGBACKREST_RESTORE_TABLESPACE_MAP"), "tablespace-map");
	opt(env.get("PGBACKREST_RESTORE_TABLESPACE_MAP_ALL"), "tablespace-map-all");
	flag("PGBACKREST_RESTORE_LINK_ALL", "link-all");
	optList(env.get("PGBACKREST_RESTORE_LINK_MAP"), "link-map");
	optList(env.get("PGBACKREST_RESTORE_DB_INCLUDE"), "db-include");
	optList(env.get("PGBACKREST_RESTORE_DB_EXCLUDE"), "db-exclude");
	optList(env.get("PGBACKREST_RESTORE_RECOVERY_OPTION"), "recovery-option");
	opt(env.get("PGBACKREST_RESTORE_ARCHIVE_MODE"), "archive-mode");
	if (env.bool("PGBACKREST_RESTORE_DELTA", true)) args.push("--delta");
	flag("PGBACKREST_RESTORE_FORCE", "force");
	const extra = env.get("PGBACKREST_RESTORE_EXTRA_OPTS");
	if (extra) args.push(...extra.split(/\s+/).filter(Boolean));
	return args;
}

function performRestore() {
	if (!PGBACKREST_ENABLE)
		throw new Error("PGBACKREST_RESTORE requires PGBACKREST_ENABLE");
	if (!fs.exists("/etc/pgbackrest.conf"))
		throw new Error("pgbackrest configuration /etc/pgbackrest.conf not found");
	fs.ensureDir(PGDATA, { mode: 0o700 });
	fs.chown(PGDATA, PG_USER + ":" + PG_GROUP);
	fs.chmod(PGDATA, 0o700);

	// Replace the shell's generate_clean_env_command (env -u PGBACKREST_*).
	const argv = ["pgbackrest"].concat(restoreArgs());
	if (
		process.run({
			process: {
				binaryPath: argv[0],
				arguments: argv.slice(1),
				filterEnvPattern: ["^PGBACKREST_"],
				user: PG_USER,
				group: PG_GROUP,
			},
		}) !== 0
	)
		throw new Error("pgBackRest restore failed");

	fs.ensureDir(PGRUN, { mode: 0o755 });
	if (fs.exists(RESTORE_SENTINEL)) fs.remove(RESTORE_SENTINEL);
	fs.write(RESTORE_COMPLETE_MARK, "restored_at=" + new Date().toISOString() + "\n", { mode: 0o600 });
}

module.exports = {
	runPg,
	clusterExists,
	clonePrimary,
	setPassword,
	createReplicationUser,
	initdb,
	performRestore,
	restoreArgs,
	isRestoreRequested,
	markRestorePending,
	cleanupStaleRestoreMarkers,
};
