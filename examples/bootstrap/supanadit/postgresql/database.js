// database.js — init/02-database: initdb, superuser password, replication
// user, HA clone, and pgBackRest restore. Mirrors the bash init/02-database.sh.
const { env, fs, editor, process, log } = require("ezx");
const {
	PGDATA,
	PGRUN,
	PGBACKUP,
	PG_USER,
	PG_GROUP,
	PG_PORT,
	PG_PASS,
	STANZA,
	PATRONI_ENABLE,
	PGBACKREST_ENABLE,
	HA_MODE,
	REPL_ROLE,
	RESTORE_SENTINEL,
} = require("./env");

// Run a one-shot command via /bin/sh as the postgres user.
function runPg(cmd, name) {
	const p = process.spawn({
		name: name || "pg",
		process: {
			binaryPath: "/bin/sh",
			arguments: ["-c", cmd],
			user: PG_USER,
			group: PG_GROUP,
		},
	});
	p.start([]);
	return p.wait();
}

function quoteArg(a) {
	return "'" + String(a).replace(/'/g, "'\\''") + "'";
}

function clusterExists() {
	return ["PG_VERSION", "postgresql.conf", "pg_hba.conf"].some((f) =>
		fs.exists(PGDATA + "/" + f),
	);
}

function clonePrimary() {
	const host = env.get("PRIMARY_HOST");
	if (!host) throw new Error("PRIMARY_HOST required for replica");
	const cmd =
		"PGPASSWORD=" +
		quoteArg(env.get("REPLICATION_PASSWORD")) +
		" pg_basebackup -h " +
		host +
		" -p " +
		env.get("PRIMARY_PORT", "5432") +
		" -U " +
		env.get("REPLICATION_USER", "replicator") +
		" -D " +
		quoteArg(PGDATA) +
		" -Fp -Xs -R";
	if (runPg(cmd, "pg_basebackup") !== 0)
		throw new Error("pg_basebackup failed");
}

function setPassword() {
	if (!PG_PASS) return;
	const cfg = fs.tempFile("/tmp", "pg_pw");
	editor
		.open(cfg)
		.replace(
			"listen_addresses = 'localhost'\nport = 5433\npassword_encryption = scram-sha-256\n",
		);
	runPg(
		"pg_ctl -D " +
			quoteArg(PGDATA) +
			' -o "--config-file=' +
			cfg +
			'" -s start',
		"pw-start",
	);
	runPg("sleep 2", "pw-wait");
	runPg(
		"psql -h localhost -p 5433 -U postgres -d postgres -c " +
			quoteArg(
				"ALTER USER postgres PASSWORD '" + PG_PASS.replace(/'/g, "''") + "';",
			),
		"pw-set",
	);
	runPg("pg_ctl -D " + quoteArg(PGDATA) + " -s stop", "pw-stop");
	fs.remove(cfg);
}

function initdb() {
	if (clusterExists()) {
		log.info("Cluster exists, skipping initdb");
		return;
	}
	if (PATRONI_ENABLE) return;
	if (HA_MODE === "native" && REPL_ROLE === "replica") {
		clonePrimary();
		return;
	}

	const args = [
		"--auth=trust",
		"--username=postgres",
		"--encoding=UTF-8",
		"--locale=C",
	];
	if (env.get("POSTGRES_INITDB_ARGS"))
		args.push(...env.get("POSTGRES_INITDB_ARGS").split(/\s+/));
	if (env.get("POSTGRES_INITDB_WALDIR"))
		args.push("--waldir=" + env.get("POSTGRES_INITDB_WALDIR"));
	args.push("--pgdata=" + PGDATA);
	if (runPg("initdb " + args.map(quoteArg).join(" "), "initdb") !== 0)
		throw new Error("initdb failed");
	fs.chmod(PGDATA, 0o700);
	setPassword();
}

// pgBackRest restore (declarative arg building from env).
function performRestore() {
	if (!PGBACKREST_ENABLE)
		throw new Error("PGBACKREST_RESTORE requires PGBACKREST_ENABLE");
	const args = [
		"--config=/etc/pgbackrest.conf",
		"--stanza=" + STANZA,
		"restore",
	];
	const opt = (v, f) => {
		if (v) args.push("--" + f + "=" + v);
	};
	const flag = (v, f) => {
		if (env.isTruthy(v)) args.push("--" + f);
	};
	opt(env.get("PGBACKREST_RESTORE_TYPE"), "type");
	opt(env.get("PGBACKREST_RESTORE_TARGET"), "target");
	opt(env.get("PGBACKREST_RESTORE_TARGET_TIMELINE"), "target-timeline");
	opt(env.get("PGBACKREST_RESTORE_TARGET_ACTION"), "target-action");
	opt(env.get("PGBACKREST_RESTORE_SET"), "set");
	opt(env.get("PGBACKREST_RESTORE_TABLESPACE_MAP"), "tablespace-map");
	opt(env.get("PGBACKREST_RESTORE_DB_INCLUDE"), "db-include");
	opt(env.get("PGBACKREST_RESTORE_DB_EXCLUDE"), "db-exclude");
	flag("PGBACKREST_RESTORE_TARGET_IMMEDIATE", "target-immediate");
	flag("PGBACKREST_RESTORE_LINK_ALL", "link-all");
	if (env.isTruthy(env.get("PGBACKREST_RESTORE_DELTA", "true")))
		args.push("--delta");
	flag("PGBACKREST_RESTORE_FORCE", "force");
	fs.mkdirAll(PGDATA, 0o700);
	fs.chmod(PGDATA, 0o700);
	if (
		runPg(
			"env -u PGBACKREST_ENABLE pgbackrest " + args.map(quoteArg).join(" "),
			"restore",
		) !== 0
	) {
		throw new Error("pgBackRest restore failed");
	}
	if (fs.exists(RESTORE_SENTINEL)) fs.remove(RESTORE_SENTINEL);
}

module.exports = {
	runPg,
	quoteArg,
	clusterExists,
	clonePrimary,
	setPassword,
	initdb,
	performRestore,
};