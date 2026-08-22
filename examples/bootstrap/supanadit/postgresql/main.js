// main.js — ezx bootstrap entrypoint for the PostgreSQL container.
//
// A 1:1 behavioural port of the production PostgreSQL container entrypoint
// (container-scripts/docker/postgresql), split into small modules mirroring
// the bash init/ runtime structure:
//
//   entrypoint.sh        -> main() below
//   init/00-misc-scripts -> (handled by the reaper / process tree)
//   init/01-directories  -> directories.js (createDirectories)
//   init/02-database     -> database.js (initdb, psql, pg_basebackup)
//   init/03-config       -> config.js (declarative FileProvision)
//   init/04-backup       -> backup.js (scheduled pgBackRest backups)
//   init/05-sshd         -> sshd.js (ssh host + replica keys)
//   runtime/startup      -> runtime.js (chain.run supervision)
//   runtime/backup       -> backup.js (cron scheduler + manual-trigger API)
//   runtime/healthcheck  -> health.readyProbe (tcp gate on /readyz)
//   runtime/shutdown     -> orchestrator ShutdownConfig + ForwardSignals
//   (reaper)             -> handled silently by ezx (PID 1 init)
//
// ezx stays PID 1 and supervises every child; it never exec's away. Requires a
// postgres image with postgres installed. Run:
//   ezx bootstrap examples/bootstrap/supanadit/postgresql/main.js
const { fs, log } = require("ezx");
const { SLEEP_MODE, RESTORE_SENTINEL } = require("./env");
const { createDirectories } = require("./directories");
const { initdb, performRestore } = require("./database");
const { sshdKeys } = require("./sshd");
const { startSleep, startDatabase } = require("./runtime");

function main() {
	fs.umask(0o027);
	createDirectories();
	initdb();

	// Config is generated declaratively when the process tree starts
	// (postgresFiles in config.js).
	sshdKeys();

	if (fs.exists(RESTORE_SENTINEL)) performRestore();

	if (SLEEP_MODE) {
		log.info("Sleep mode");
		startSleep();
		return;
	}
	startDatabase();
	log.info("Entrypoint complete");
}

main();