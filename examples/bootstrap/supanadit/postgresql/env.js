// env.js — shared environment defaults for the PostgreSQL container.
// Reads the container environment once and exports the constants used across
// the bootstrap modules. Single source of truth: change a default here, not
// in every file.
const { env } = require("ezx");

// Directories.
const PGDATA = env.get("PGDATA", "/opt/containers/data");
const PGCONFIG = env.get("PGCONFIG", "/opt/containers/config");
const PGLOG = env.get("PGLOG", "/opt/containers/logs");
const PGRUN = env.get("PGRUN", "/opt/containers/run");
const PGBACKUP = env.get("PGBACKUP", "/opt/containers/backup");

// PostgreSQL identity / connection defaults.
const PG_USER = env.get("POSTGRES_USER", "postgres");
const PG_GROUP = env.get("POSTGRES_GROUP", "postgres");
const PG_DB = env.get("POSTGRES_DB", "postgres");
const PG_PORT = env.get("POSTGRESQL_PORT", "5432");
const PG_TZ = env.get("POSTGRESQL_TIMEZONE", "UTC");
const PG_PASS = env.get("POSTGRES_PASSWORD");
const PG_HOST_AUTH_METHOD = env.get("POSTGRES_HOST_AUTH_METHOD", "trust");
const PG_INITDB_ARGS = env.get("POSTGRES_INITDB_ARGS", "");
const PG_INITDB_WALDIR = env.get("POSTGRES_INITDB_WALDIR", "");
const STANZA = env.get("PGBACKREST_STANZA", "default");

// Feature flags (truthy booleans).
const PGBOUNCER_ENABLE = env.bool("PGBOUNCER_ENABLE");
const PATRONI_ENABLE = env.bool("PATRONI_ENABLE");
const PGBACKREST_ENABLE = env.bool("PGBACKREST_ENABLE");
const PGBACKREST_AUTO = env.bool("PGBACKREST_AUTO_ENABLE");
const PGBACKREST_ARCHIVE = env.bool("PGBACKREST_ARCHIVE_ENABLE", true);
const SLEEP_MODE = env.bool("SLEEP_MODE");
const CITUS_ENABLE = env.bool("CITUS_ENABLE");

// HA / replication role.
const HA_MODE = env.get("HA_MODE", "");
const REPL_ROLE = env.get("REPLICATION_ROLE", "");
const REPL_USER = env.get("REPLICATION_USER", "replicator");
const REPL_PASSWORD = env.get("REPLICATION_PASSWORD", "");
const PRIMARY_HOST = env.get("PRIMARY_HOST", "");
const PRIMARY_PORT = env.get("PRIMARY_PORT", "5432");
const REPL_SYNC_MODE = env.get("REPLICATION_SYNCHRONOUS_MODE", "true");
const REPL_SYNC_COUNT = env.get("REPLICATION_SYNCHRONOUS_COUNT", "");
const REPL_SYNC_REPLICAS = env.get("REPLICATION_SYNCHRONOUS_REPLICAS", "");

// CITUS.
const CITUS_GROUP = env.get("CITUS_GROUP", "");
const CITUS_DATABASE = env.get("CITUS_DATABASE", "postgres");
const CITUS_ROLE = env.get("CITUS_ROLE", "");

// PgBouncer.
const PGBOUNCER_LISTEN_ADDR = env.get("PGBOUNCER_LISTEN_ADDR", "0.0.0.0");
const PGBOUNCER_LISTEN_PORT = env.get("PGBOUNCER_LISTEN_PORT", "6432");
const PGBOUNCER_AUTH_TYPE = env.get("PGBOUNCER_AUTH_TYPE", "md5");
const PGBOUNCER_ADMIN_USERS = env.get("PGBOUNCER_ADMIN_USERS", "postgres");
const PGBOUNCER_STATS_USERS = env.get("PGBOUNCER_STATS_USERS", "postgres");
const PGBOUNCER_POOL_MODE = env.get("PGBOUNCER_POOL_MODE", "transaction");
const PGBOUNCER_MAX_CLIENT_CONN = env.get("PGBOUNCER_MAX_CLIENT_CONN", "100");
const PGBOUNCER_DEFAULT_POOL_SIZE = env.get("PGBOUNCER_DEFAULT_POOL_SIZE", "20");
const PGBOUNCER_INI_TEMPLATE = "/etc/pgbouncer/pgbouncer.ini";

// Timing / timeouts.
const TIMEOUT = env.int("TIMEOUT", 30);
const PG_READY_MAX_ATTEMPTS = env.int("POSTGRESQL_READY_MAX_ATTEMPTS", 30);
const PG_READY_ATTEMPT_INTERVAL = env.int("POSTGRESQL_READY_ATTEMPT_INTERVAL", 1);
const PGBACKREST_AUTO_TIMEZONE = env.get("PGBACKREST_AUTO_TIMEZONE", "UTC");
const PGBACKREST_AUTO_FIRST_INCR_DELAY = env.int("PGBACKREST_AUTO_FIRST_INCR_DELAY", 120);

// Logging.
const LOG_LEVEL = env.get("LOG_LEVEL", "INFO");

// Well-known file paths.
const POSTGRES_CONF = PGDATA + "/postgresql.conf";
const PG_HBA = PGDATA + "/pg_hba.conf";
const PG_PASS_FILE = PGDATA + "/pgpass";
const PGBACKREST_CONF = "/etc/pgbackrest.conf";
const PATRONI_CONF = "/etc/patroni.yml";
const SSHD_CONF = "/etc/ssh/sshd_config";
const PGBOUNCER_INI = PGBOUNCER_INI_TEMPLATE;
const PGBOUNCER_USERLIST = "/etc/pgbouncer/userlist.txt";
const RESTORE_SENTINEL = PGRUN + "/pgbackrest-restore.pending";
const RESTORE_COMPLETE_MARK = PGRUN + "/pgbackrest-restore.complete";

module.exports = {
	PGDATA,
	PGCONFIG,
	PGLOG,
	PGRUN,
	PGBACKUP,
	PG_USER,
	PG_GROUP,
	PG_DB,
	PG_PORT,
	PG_TZ,
	PG_PASS,
	PG_HOST_AUTH_METHOD,
	PG_INITDB_ARGS,
	PG_INITDB_WALDIR,
	STANZA,
	PGBOUNCER_ENABLE,
	PATRONI_ENABLE,
	PGBACKREST_ENABLE,
	PGBACKREST_AUTO,
	PGBACKREST_ARCHIVE,
	SLEEP_MODE,
	CITUS_ENABLE,
	CITUS_GROUP,
	CITUS_DATABASE,
	CITUS_ROLE,
	HA_MODE,
	REPL_ROLE,
	REPL_USER,
	REPL_PASSWORD,
	PRIMARY_HOST,
	PRIMARY_PORT,
	REPL_SYNC_MODE,
	REPL_SYNC_COUNT,
	REPL_SYNC_REPLICAS,
	PGBOUNCER_LISTEN_ADDR,
	PGBOUNCER_LISTEN_PORT,
	PGBOUNCER_AUTH_TYPE,
	PGBOUNCER_ADMIN_USERS,
	PGBOUNCER_STATS_USERS,
	PGBOUNCER_POOL_MODE,
	PGBOUNCER_MAX_CLIENT_CONN,
	PGBOUNCER_DEFAULT_POOL_SIZE,
	TIMEOUT,
	PG_READY_MAX_ATTEMPTS,
	PG_READY_ATTEMPT_INTERVAL,
	PGBACKREST_AUTO_TIMEZONE,
	PGBACKREST_AUTO_FIRST_INCR_DELAY,
	LOG_LEVEL,
	POSTGRES_CONF,
	PG_HBA,
	PG_PASS_FILE,
	PGBACKREST_CONF,
	PATRONI_CONF,
	SSHD_CONF,
	PGBOUNCER_INI,
	PGBOUNCER_USERLIST,
	RESTORE_SENTINEL,
	RESTORE_COMPLETE_MARK,
};
