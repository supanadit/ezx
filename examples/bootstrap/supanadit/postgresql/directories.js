// directories.js — init/01-directories: create and secure the container
// data/config/run/backup directories (mirrors the bash init/01-directories.sh).
const { fs } = require("ezx");
const { PGDATA, PGCONFIG, PGRUN, PGBACKUP, PGBACKREST_ENABLE } = require("./env");

function createDirectories() {
	for (const d of [PGDATA, PGCONFIG, PGRUN]) {
		fs.mkdirAll(d, 0o755);
		fs.chmod(d, 0o755);
	}
	fs.chmod(PGDATA, 0o700);
	if (PGBACKREST_ENABLE) {
		fs.mkdirAll(PGBACKUP, 0o750);
		fs.chmod(PGBACKUP, 0o750);
		for (const s of ["spool", "log", "lock"]) {
			fs.mkdirAll(PGBACKUP + "/" + s, 0o750);
			fs.chmod(PGBACKUP + "/" + s, 0o750);
		}
	}
}

module.exports = { createDirectories };