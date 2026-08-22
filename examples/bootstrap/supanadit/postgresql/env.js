// env.js — shared environment defaults for the PostgreSQL container.
// Reads the container environment once and exports the constants used across
// the bootstrap modules. Single source of truth: change a default here, not
// in every file.
const { env } = require("ezx");

// Directories.
const PGDATA = env.get("PGDATA", "/opt/containers/data");
const PGCONFIG = env.get("PGCONFIG", "/opt/containers/config");
const PGRUN = env.get("PGRUN", "/opt/containers/run");
const PGBACKUP = env.get("PGBACKUP", "/opt/containers/backup");

// PostgreSQL identity / connection defaults.
const PG_USER = env.get("POSTGRES_USER", "postgres");
const PG_GROUP = env.get("POSTGRES_GROUP", "postgres");
const PG_PORT = env.get("POSTGRESQL_PORT", "5432");
const PG_TZ = env.get("POSTGRESQL_TIMEZONE", "UTC");
const PG_PASS = env.get("POSTGRES_PASSWORD");
const STANZA = env.get("PGBACKREST_STANZA", "default");

// Feature flags (truthy booleans).
const PGBOUNCER_ENABLE = env.isTruthy("PGBOUNCER_ENABLE");
const PATRONI_ENABLE = env.isTruthy("PATRONI_ENABLE");
const PGBACKREST_ENABLE = env.isTruthy("PGBACKREST_ENABLE");
const PGBACKREST_AUTO = env.isTruthy("PGBACKREST_AUTO_ENABLE");
const SLEEP_MODE = env.isTruthy("SLEEP_MODE");

// HA / replication role.
const HA_MODE = env.get("HA_MODE", "");
const REPL_ROLE = env.get("REPLICATION_ROLE", "");

// Well-known file paths.
const POSTGRES_CONF = PGDATA + "/postgresql.conf";
const PG_HBA = PGDATA + "/pg_hba.conf";
const PGBACKREST_CONF = "/etc/pgbackrest.conf";
const PATRONI_CONF = "/etc/patroni.yml";
const SSHD_CONF = "/etc/ssh/sshd_config";
const PGBOUNCER_INI = "/etc/pgbouncer/pgbouncer.ini";
const RESTORE_SENTINEL = PGRUN + "/pgbackrest-restore.pending";

module.exports = {
	PGDATA,
	PGCONFIG,
	PGRUN,
	PGBACKUP,
	PG_USER,
	PG_GROUP,
	PG_PORT,
	PG_TZ,
	PG_PASS,
	STANZA,
	PGBOUNCER_ENABLE,
	PATRONI_ENABLE,
	PGBACKREST_ENABLE,
	PGBACKREST_AUTO,
	SLEEP_MODE,
	HA_MODE,
	REPL_ROLE,
	POSTGRES_CONF,
	PG_HBA,
	PGBACKREST_CONF,
	PATRONI_CONF,
	SSHD_CONF,
	PGBOUNCER_INI,
	RESTORE_SENTINEL,
};