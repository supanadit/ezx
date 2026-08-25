// validation.js — port of the bash utils/validation.sh. Validates the
// container environment, HA configuration, and required dependencies before
// any side effects. YAML validation of patroni.yml is skipped: ezx's
// yaml.build guarantees structural validity at generation time.
const { env, log, scheduler, fs } = require("ezx");

// validateCronExpression — delegate to ezx's cron engine so validation always
// matches what the scheduler actually runs.
function validateCronExpression(expr) {
	try {
		scheduler.parse(expr);
		return true;
	} catch {
		return false;
	}
}

// validateMemoryValue — accepts "256MB", "1GB", "512", "2TB", etc.
function validateMemoryValue(value) {
	return /^[0-9]+(kB|MB|GB|TB)?$/.test(String(value || ""));
}

function validateEnvironment() {
	let failed = false;
	const fail = (msg) => {
		log.error(msg);
		failed = true;
	};
	const warn = (msg) => log.warn(msg);

	// LOG_LEVEL
	const logLevel = env.get("LOG_LEVEL", "INFO").toUpperCase();
	if (!["DEBUG", "INFO", "WARN", "ERROR"].includes(logLevel)) {
		fail(`Invalid LOG_LEVEL: ${env.get("LOG_LEVEL")} (must be DEBUG, INFO, WARN, or ERROR)`);
	}

	// TIMEOUT
	const timeout = env.get("TIMEOUT", "30");
	if (!/^[0-9]+$/.test(timeout) || env.int("TIMEOUT", 0) <= 0) {
		fail(`Invalid TIMEOUT: ${timeout} (must be a positive integer)`);
	}

	// REPLICATION_SYNCHRONOUS_MODE
	const syncMode = env.get("REPLICATION_SYNCHRONOUS_MODE", "true");
	if (!["true", "false"].includes(syncMode)) {
		fail(`Invalid REPLICATION_SYNCHRONOUS_MODE: ${syncMode} (must be true or false)`);
	}

	// REPLICATION_SYNCHRONOUS_COUNT
	if (syncMode === "true" && env.get("REPLICATION_SYNCHRONOUS_COUNT")) {
		const count = env.get("REPLICATION_SYNCHRONOUS_COUNT");
		if (!/^[0-9]+$/.test(count) || env.int("REPLICATION_SYNCHRONOUS_COUNT", 0) < 1) {
			fail(`Invalid REPLICATION_SYNCHRONOUS_COUNT: ${count} (must be a positive integer)`);
		}
	}

	// SLEEP_MODE
	const sleepMode = env.get("SLEEP_MODE", "false");
	if (!["true", "false"].includes(sleepMode.toLowerCase())) {
		fail(`Invalid SLEEP_MODE: ${sleepMode} (must be true or false)`);
	}

	// PGBACKREST_ENABLE lenient check
	if (env.get("PGBACKREST_ENABLE") && !env.bool("PGBACKREST_ENABLE")) {
		warn(`PGBACKREST_ENABLE='${env.get("PGBACKREST_ENABLE")}' is not recognized as true; treating as false`);
	}

	// Cross-variable warnings
	if (!env.bool("PGBACKREST_ENABLE")) {
		if (env.bool("PGBACKREST_AUTO_ENABLE")) {
			warn("PGBACKREST_AUTO_ENABLE=true is ignored because PGBACKREST_ENABLE=false");
		}
		if (env.bool("PGBACKREST_RESTORE")) {
			warn("PGBACKREST_RESTORE=true requires PGBACKREST_ENABLE=true; restore will be disabled");
		}
		if (env.bool("PGBACKREST_ARCHIVE_ENABLE", true)) {
			warn("PGBACKREST_ARCHIVE_ENABLE=true has no effect when PGBACKREST_ENABLE=false; archiving will be disabled");
		}
	}

	if (env.bool("PGBACKREST_ENABLE")) {
		// Auto-backup feature lenient checks
		if (env.get("PGBACKREST_AUTO_ENABLE") && !env.bool("PGBACKREST_AUTO_ENABLE")) {
			warn(`PGBACKREST_AUTO_ENABLE='${env.get("PGBACKREST_AUTO_ENABLE")}' not recognized; using default (false)`);
		}
		if (env.get("PGBACKREST_AUTO_PRIMARY_ONLY") && !env.bool("PGBACKREST_AUTO_PRIMARY_ONLY", true)) {
			warn(`PGBACKREST_AUTO_PRIMARY_ONLY='${env.get("PGBACKREST_AUTO_PRIMARY_ONLY")}' not recognized; using default (true)`);
		}

		// Cron expressions
		for (const cronVar of ["PGBACKREST_AUTO_FULL_CRON", "PGBACKREST_AUTO_DIFF_CRON", "PGBACKREST_AUTO_INCR_CRON"]) {
			const val = env.get(cronVar);
			if (val && !validateCronExpression(val)) {
				fail(`Invalid ${cronVar}: ${val} (must be valid 5-field cron expression)`);
			}
		}

		// First incremental delay
		const firstIncr = env.get("PGBACKREST_AUTO_FIRST_INCR_DELAY");
		if (firstIncr && (!/^[0-9]+$/.test(firstIncr) || env.int("PGBACKREST_AUTO_FIRST_INCR_DELAY", 0) <= 0)) {
			fail(`Invalid PGBACKREST_AUTO_FIRST_INCR_DELAY: ${firstIncr} (must be positive integer seconds)`);
		}

		// Stanza primary wait
		const stanzaWait = env.get("PGBACKREST_STANZA_PRIMARY_WAIT");
		if (stanzaWait && (!/^[0-9]+$/.test(stanzaWait) || env.int("PGBACKREST_STANZA_PRIMARY_WAIT", 0) < 0)) {
			fail(`Invalid PGBACKREST_STANZA_PRIMARY_WAIT: ${stanzaWait} (must be non-negative integer seconds)`);
		}

		// Repo type
		const repoType = env.get("PGBACKREST_REPO_TYPE", "posix");
		if (!["posix", "filesystem", "s3", "gcs", "sftp"].includes(repoType)) {
			fail(`Invalid PGBACKREST_REPO_TYPE: ${repoType} (must be posix|filesystem|s3|gcs|sftp)`);
		}

		if (repoType === "s3") {
			if (!env.get("PGBACKREST_REPO_S3_BUCKET")) {
				fail("PGBACKREST_REPO_S3_BUCKET is required when PGBACKREST_REPO_TYPE=s3");
			}
			if (!env.get("PGBACKREST_REPO_S3_ENDPOINT")) {
				fail("PGBACKREST_REPO_S3_ENDPOINT is required when PGBACKREST_REPO_TYPE=s3");
			}
			if (!env.get("PGBACKREST_REPO_S3_KEY") || !env.get("PGBACKREST_REPO_S3_KEY_SECRET")) {
				warn("S3 key or secret not provided; ensure alternative auth (IAM/anonymous) is configured if required");
			}
			const s3Port = env.get("PGBACKREST_REPO_S3_PORT");
			if (s3Port && !/^[0-9]+$/.test(s3Port)) {
				fail(`Invalid PGBACKREST_REPO_S3_PORT: ${s3Port} (must be numeric)`);
			}
			const s3VerifyTls = env.get("PGBACKREST_REPO_S3_VERIFY_TLS");
			if (s3VerifyTls && !["true", "TRUE", "false", "FALSE", "1", "0", "y", "Y", "n", "N"].includes(s3VerifyTls)) {
				fail(`Invalid PGBACKREST_REPO_S3_VERIFY_TLS: ${s3VerifyTls} (must be boolean-like)`);
			}
		} else if (repoType === "gcs") {
			if (!env.get("PGBACKREST_REPO_GCS_BUCKET")) {
				fail("PGBACKREST_REPO_GCS_BUCKET is required when PGBACKREST_REPO_TYPE=gcs");
			}
			const gcsKeyType = env.get("PGBACKREST_REPO_GCS_KEY_TYPE");
			if (gcsKeyType && !["auto", "service", "token"].includes(gcsKeyType)) {
				fail(`Invalid PGBACKREST_REPO_GCS_KEY_TYPE: ${gcsKeyType} (must be auto|service|token)`);
			}
		} else if (repoType === "sftp") {
			if (!env.get("PGBACKREST_REPO_SFTP_HOST")) {
				fail("PGBACKREST_REPO_SFTP_HOST is required when PGBACKREST_REPO_TYPE=sftp");
			}
			const sftpPort = env.get("PGBACKREST_REPO_SFTP_HOST_PORT");
			if (sftpPort && !/^[0-9]+$/.test(sftpPort)) {
				fail(`Invalid PGBACKREST_REPO_SFTP_HOST_PORT: ${sftpPort} (must be numeric)`);
			}
			const sftpKeyCheck = env.get("PGBACKREST_REPO_SFTP_HOST_KEY_CHECK_TYPE");
			if (sftpKeyCheck && !["strict", "accept-new", "fingerprint", "none"].includes(sftpKeyCheck)) {
				fail(`Invalid PGBACKREST_REPO_SFTP_HOST_KEY_CHECK_TYPE: ${sftpKeyCheck}`);
			}
		}
	}

	// PostgreSQL-specific variables
	const sharedBuffers = env.get("POSTGRESQL_SHARED_BUFFERS");
	if (sharedBuffers && !validateMemoryValue(sharedBuffers)) {
		fail(`Invalid POSTGRESQL_SHARED_BUFFERS: ${sharedBuffers}`);
	}
	const maxConn = env.get("POSTGRESQL_MAX_CONNECTIONS");
	if (maxConn && (!/^[0-9]+$/.test(maxConn) || env.int("POSTGRESQL_MAX_CONNECTIONS", 0) <= 0)) {
		fail(`Invalid POSTGRESQL_MAX_CONNECTIONS: ${maxConn}`);
	}

	return !failed;
}

// validateHaConfiguration — HA_MODE=native exclusivity + role/primary checks.
function validateHaConfiguration() {
	let failed = false;
	const fail = (msg) => {
		log.error(msg);
		failed = true;
	};

	if (env.get("HA_MODE", "") === "native") {
		if (env.bool("PATRONI_ENABLE")) {
			fail("HA_MODE=native cannot be used with PATRONI_ENABLE=true");
		}
		const role = env.get("REPLICATION_ROLE", "");
		if (role !== "primary" && role !== "replica") {
			fail(`Invalid REPLICATION_ROLE: ${role} (must be primary or replica)`);
		}
		if (role === "replica" && !env.get("PRIMARY_HOST")) {
			fail("PRIMARY_HOST must be set for replica role");
		}
	}
	return !failed;
}

// validateDependencies — required binaries exist before starting.
function validateDependencies() {
	let failed = false;
	const fail = (msg) => {
		log.error(msg);
		failed = true;
	};

	const required = ["pg_ctl", "initdb", "psql"];
	if (env.bool("PATRONI_ENABLE")) required.push("patroni");
	if (env.bool("PGBACKREST_ENABLE")) required.push("pgbackrest");
	if (env.bool("PGBOUNCER_ENABLE")) required.push("pgbouncer");

	for (const cmd of required) {
		if (!env.has(cmd.toUpperCase().replace(/[^A-Z0-9]/g, "_") + "_PATH") && !commandExists(cmd)) {
			fail(`Required command not found: ${cmd}`);
		}
	}
	return !failed;
}

// commandExists — true when the binary resolves via fs.which.
function commandExists(cmd) {
	return !!fs.which(cmd);
}

module.exports = {
	validateEnvironment,
	validateHaConfiguration,
	validateDependencies,
	validateCronExpression,
	validateMemoryValue,
};
