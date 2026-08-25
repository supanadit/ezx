// main.js — ezx bootstrap entrypoint for the PostgreSQL container.
//
// A 1:1 behavioural port of the production PostgreSQL container entrypoint
// (container-scripts/docker/postgresql), split into small modules mirroring
// the bash init/ runtime structure:
//
//   entrypoint.sh        -> main() below
//   init/00-misc-scripts -> (handled by the reaper / process tree)
//   init/01-directories  -> setupDirectories() below
//   init/02-database     -> database.js (initdb, psql, pg_basebackup)
//   init/03-config       -> config.js (declarative FileProvision)
//   init/04-backup       -> runtime.js backup node-builders (scheduled pgBackRest)
//   init/05-sshd         -> setupSshd() below
//   runtime/startup      -> runtime.js (chain.run supervision)
//   runtime/backup       -> api.js (cron scheduler + manual-trigger API)
//   runtime/healthcheck  -> health.readyProbe (tcp gate on /readyz)
//   runtime/shutdown     -> orchestrator ShutdownConfig + ForwardSignals
//   (reaper)             -> handled silently by ezx (PID 1 init)
//
// ezx stays PID 1 and supervises every child; it never exec's away. Requires a
// postgres image with postgres installed. Run:
//   ezx bootstrap examples/bootstrap/supanadit/postgresql/main.js
const { fs, editor, log } = require("ezx");
const { SLEEP_MODE, RESTORE_SENTINEL, PG_USER, PG_GROUP } = require("./env");
const { initdb, performRestore } = require("./database");
const { startSleep, startDatabase } = require("./runtime");
const {
	validateEnvironment,
	validateHaConfiguration,
	validateDependencies,
} = require("./validation");
const { registerOpsRoutes } = require("./api");

function runAsPostgres(argv, name) {
	return require("ezx").process.run({
		name: name || "pg",
		process: {
			binaryPath: argv[0],
			arguments: argv.slice(1),
			user: PG_USER,
			group: PG_GROUP,
		},
	});
}

// setupDirectories — init/01-directories: create and secure the container
// data/config/log/run/backup directories and chown them to the postgres user
// (mirrors the bash init/01-directories.sh).
function setupDirectories() {
	const { PGDATA, PGCONFIG, PGLOG, PGRUN, PGBACKUP, PGBACKREST_ENABLE } = require("./env");
	const owner = PG_USER + ":" + PG_GROUP;

	for (const d of [PGCONFIG, PGLOG, PGRUN]) {
		fs.ensureDir(d, { mode: 0o755 });
		fs.chmod(d, 0o755);
	}

	fs.ensureDir(PGDATA, { mode: 0o700 });
	fs.chmod(PGDATA, 0o700);

	if (PGBACKREST_ENABLE) {
		fs.ensureDir(PGBACKUP, { mode: 0o750 });
		fs.chmod(PGBACKUP, 0o750);
		for (const s of ["spool", "log", "lock"]) {
			fs.ensureDir(PGBACKUP + "/" + s, { mode: 0o750 });
			fs.chmod(PGBACKUP + "/" + s, 0o750);
		}
	}

	// Ownership: everything must be writable by the postgres process (bash
	// 01-directories.sh chowns -R postgres:postgres).
	for (const d of [PGDATA, PGCONFIG, PGLOG, PGRUN, PGBACKUP]) {
		if (fs.exists(d)) fs.chownRecursive(d, owner);
	}
}

// setupSshd — init/05-sshd: generate host keys, move them to /run/ssh, and
// generate replica SSH keys (mirrors the bash init/05-sshd.sh).
function setupSshd() {
	const run = "/run/ssh";
	fs.ensureDir(run, { mode: 0o755 });

	// sshd privilege-separation directory (distinct from /run/ssh where host
	// keys live). /run is tmpfs and empty on each container start, so sshd
	// fails with "Missing privilege separation directory: /run/sshd" without it.
	fs.ensureDir("/run/sshd", { mode: 0o755 });

	// Generate host keys once, then move them to /run/ssh with root ownership
	// and strict modes (bash 05-sshd.sh:27-41). ssh-keygen -A writes to
	// /etc/ssh/ssh_host_*.
	if (!fs.exists(run + "/ssh_host_rsa_key")) {
		runAsPostgres(["ssh-keygen", "-A"], "ssh-keygen");
		for (const base of ["rsa", "ecdsa", "ed25519"]) {
			const src = "/etc/ssh/ssh_host_" + base + "_key";
			const pub = src + ".pub";
			if (fs.exists(src)) {
				fs.rename(src, run + "/ssh_host_" + base + "_key");
				fs.chmod(run + "/ssh_host_" + base + "_key", 0o600);
			}
			if (fs.exists(pub)) {
				fs.rename(pub, run + "/ssh_host_" + base + "_key.pub");
				fs.chmod(run + "/ssh_host_" + base + "_key.pub", 0o644);
			}
		}
		fs.chown(run, "root:root");
	}

	// Replica keypair for SSH-based pgBackRest standby.
	const dir = "/home/postgres/.ssh";
	fs.ensureDir(dir, { mode: 0o700 });
	fs.chmod(dir, 0o700);
	fs.chown(dir, "postgres:" + PG_GROUP);

	const keyFile = dir + "/id_rsa";
	if (!fs.exists(keyFile)) {
		if (!fs.which("ssh-keygen")) {
			log.warn("ssh-keygen not found, skipping SSH key generation");
			return;
		}
		runAsPostgres(
			["ssh-keygen", "-t", "rsa", "-b", "4096", "-f", keyFile, "-N", "", "-C", "postgres@replica-auth"],
			"ssh-key",
		);
		fs.chmod(keyFile, 0o600);
		fs.chown(keyFile, "postgres:" + PG_GROUP);
	}

	const pubKey = keyFile + ".pub";
	if (fs.exists(pubKey)) fs.chmod(pubKey, 0o644);

	// Append the public key to authorized_keys (accumulates, like bash).
	const authFile = dir + "/authorized_keys";
	if (!fs.exists(authFile)) {
		fs.write(authFile, "", { mode: 0o600 });
		fs.chown(authFile, "postgres:" + PG_GROUP);
		fs.chmod(authFile, 0o600);
	}
	if (fs.exists(pubKey)) {
		const pub = editor.open(pubKey).read().trim();
		const auth = editor.open(authFile).read();
		if (!auth.includes(pub)) {
			fs.write(authFile, auth + pub + "\n", { mode: 0o600 });
		}
	}
	fs.chown(authFile, "postgres:" + PG_GROUP);
	fs.chmod(authFile, 0o600);
}

function main() {
	fs.umask(0o027);

	// Validate environment, HA config, and required dependencies before any
	// side effects (mirrors entrypoint.sh: validate_environment +
	// validate_ha_configuration + validate_dependencies).
	if (!validateEnvironment()) {
		throw new Error("Environment validation failed");
	}
	if (!validateHaConfiguration()) {
		throw new Error("HA configuration validation failed");
	}
	if (!validateDependencies()) {
		throw new Error("Dependency validation failed");
	}

	setupDirectories();
	initdb();

	// Config is generated declaratively when the process tree starts
	// (postgresFiles in config.js).
	setupSshd();

	// If initdb marked a restore pending (PGBACKREST_RESTORE=true), run the
	// pgBackRest restore before starting the database.
	if (fs.exists(RESTORE_SENTINEL)) performRestore();

	if (SLEEP_MODE) {
		log.info("Sleep mode");
		startSleep();
		return;
	}

	// Register operator-facing routes (sync-reload, composite health) and the
	// scheduler.every Patroni role-check callback on the health server before
	// the chain starts supervising.
	registerOpsRoutes();
	startDatabase();
	log.info("Entrypoint complete");
}

main();
