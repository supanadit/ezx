// ezx bootstrap examples/bootstrap/postgres.js
//
// A port of the official PostgreSQL docker-entrypoint.sh, driven
// entirely through the ezx JS host API. It covers:
//   1. docker_setup_env  → env.get with defaults
//   2. first-run detection → fs.exists("$PGDATA/PG_VERSION")
//   3. directory + permissions → fs.mkdirAll/chmod/chownRecursive (when root)
//   4. initdb  → process.spawn as the postgres user (privilege drop)
//   5. pg_hba.conf → editor
//   6. temp server start + psql setup + init scripts → process.spawn
//   7. final exec → process.exec (postgres becomes PID 1)
//
// To run against a real postgres image (e.g. docker run --entrypoint ezx ...):
//   go build -o ezx ./app
//   docker run --rm -e POSTGRES_PASSWORD=secret -v $(pwd)/ezx:/usr/local/bin/ezx \
//     -v $(pwd)/examples:/examples postgres:16 \
//     ezx bootstrap /examples/postgres.js
const { env, fs, editor, process, log } = require("ezx");

// ---- docker_setup_env -------------------------------------------------------
const PGDATA = env.get("PGDATA", "/var/lib/postgresql/data");
const POSTGRES_USER = env.get("POSTGRES_USER", "postgres");
const POSTGRES_DB = env.get("POSTGRES_DB", POSTGRES_USER);
const POSTGRES_PASSWORD = env.get("POSTGRES_PASSWORD");
const POSTGRES_HOST_AUTH_METHOD = env.get("POSTGRES_HOST_AUTH_METHOD");
const POSTGRES_INITDB_ARGS = env.get("POSTGRES_INITDB_ARGS", "");
const POSTGRES_INITDB_WALDIR = env.get("POSTGRES_INITDB_WALDIR");

// Running as root (id 0)? Containers started with USER postgres set this to
// false; the default root entrypoint sets EZX_RUN_AS_ROOT=true.
const runningAsRoot = env.get("EZX_RUN_AS_ROOT", "false") === "true";

// ---- docker_verify_minimum_env ---------------------------------------------
if (!POSTGRES_PASSWORD && POSTGRES_HOST_AUTH_METHOD !== "trust") {
  throw new Error("The database is not initialized, and you have not set POSTGRES_PASSWORD. Please set it, or set POSTGRES_HOST_AUTH_METHOD=trust.");
}

// ---- data directory + permissions (when root) --------------------------------
fs.mkdirAll(PGDATA, 0o700);
if (runningAsRoot) {
  fs.chownRecursive(PGDATA, POSTGRES_USER + ":" + POSTGRES_USER);
  fs.mkdirAll("/var/run/postgresql", 0o775);
  fs.chown("/var/run/postgresql", POSTGRES_USER + ":" + POSTGRES_USER);
}

// ---- first-run detection ----------------------------------------------------
const databaseAlreadyExists = fs.exists(PGDATA + "/PG_VERSION");

if (!databaseAlreadyExists) {
  log.info("PostgreSQL init process complete check: DATABASE_ALREADY_EXISTS=false");

  // ---- docker_init_database_dir (initdb) ------------------------------------
  const initArgs = [
    "--username=" + POSTGRES_USER,
    "--pwfile=/tmp/ezx-pwfile",
  ];
  if (POSTGRES_INITDB_WALDIR) {
    initArgs.push("--waldir=" + POSTGRES_INITDB_WALDIR);
  }
  if (POSTGRES_INITDB_ARGS) {
    // split on spaces, preserving simple quoting
    for (const part of POSTGRES_INITDB_ARGS.split(/\s+/)) {
      if (part) initArgs.push(part);
    }
  }
  initArgs.push(PGDATA);

  // Write the password file (initdb reads it; refuse a properly-empty file)
  fs.mkdirAll("/tmp", 0o1777);
  editor.open("/tmp/ezx-pwfile").replace((POSTGRES_PASSWORD || "") + "\n");

  log.info("Running initdb as user %s", POSTGRES_USER);
  process.run({
    name: "initdb",
    process: {
      binaryPath: env.get("EZX_POSTGRES_BIN", "/usr/local/bin/initdb"),
      arguments: initArgs,
      user: POSTGRES_USER,           // privilege drop (Phase B)
      group: POSTGRES_USER,
    },
    check: true,
  });
  fs.remove("/tmp/ezx-pwfile");

  // ---- pg_setup_hba_conf -----------------------------------------------------
  const hba = editor.open(PGDATA + "/pg_hba.conf");
  hba.remove(/^host all all all /);
  const method = POSTGRES_HOST_AUTH_METHOD || "trust";
  hba.append("host all all all " + method);
  hba.append("local all all " + method);

  // ---- docker_temp_server_start ----------------------------------------------
  // Trust auth for the temp server on a private socket.
  const tempArgs = [
    "-D", PGDATA,
    "-c", "listen_addresses=''",
    "-c", "unix_socket_directories=/var/run/postgresql",
    "-c", "auth_method=trust",
  ];
  const tempStart = process.spawn({
    name: "postgres-temp",
    process: {
      binaryPath: env.get("EZX_POSTGRES_BIN_DIR", "/usr/local/bin/postgres"),
      arguments: tempArgs,
      user: POSTGRES_USER,
      group: POSTGRES_USER,
    },
  });
  tempStart.start([]);
  log.info("Started temporary postgres server (socket)");

  // ---- docker_setup_db --------------------------------------------------------
  const psqlArgs = ["--username", POSTGRES_USER, "--dbname", POSTGRES_DB];
  process.run({
    name: "psql-setup",
    process: {
      binaryPath: "/usr/bin/psql",
      arguments: psqlArgs.concat(["-c", "SELECT 1"]),
      environment: ["PGPASSWORD=" + (POSTGRES_PASSWORD || "")],
      user: POSTGRES_USER,
    },
  });

  // ---- docker_process_init_files (sorted .sql / .sh) ---------------------------
  const initDir = "/docker-entrypoint-initdb.d";
  if (fs.exists(initDir)) {
    for (const name of fs.readDir(initDir)) {
      const full = initDir + "/" + name;
      if (name.endsWith(".sql")) {
        log.info("running %s", full);
        process.run({
          name: "init-" + name,
          process: {
            binaryPath: "/usr/bin/psql",
            arguments: ["--username", POSTGRES_USER, "--dbname", POSTGRES_DB, "-f", full],
            environment: ["PGPASSWORD=" + (POSTGRES_PASSWORD || "")],
            user: POSTGRES_USER,
          },
          check: true,
        });
      } else if (name.endsWith(".sql.gz")) {
        log.info("running (gzip) %s", full);
        process.run({
          name: "gunzip-" + name,
          process: {
            binaryPath: "/bin/sh",
            arguments: ["-c", "gunzip -c " + full + " | psql --username " + POSTGRES_USER + " --dbname " + POSTGRES_DB],
            environment: ["PGPASSWORD=" + (POSTGRES_PASSWORD || "")],
            user: POSTGRES_USER,
          },
          check: true,
        });
      } else if (name.endsWith(".sh")) {
        log.info("running (shell) %s", full);
        process.run({
          name: "sh-" + name,
          process: {
            binaryPath: "/bin/sh",
            arguments: [full],
            user: POSTGRES_USER,
          },
          check: true,
        });
      }
    }
  }

  // ---- docker_temp_server_stop -------------------------------------------------
  tempStart.signal("SIGINT");   // fast shutdown
  tempStart.wait();

  log.info("PostgreSQL init process complete; ready for start up.");
} else {
  log.info("PostgreSQL Database directory appears to contain a database; skipping initialization");
}

// ---- exec "$@" : postgres becomes PID 1 ----------------------------------------
// This never returns on success: ezx's process image is replaced by postgres.
process.exec({
  name: "postgres",
  process: {
    binaryPath: env.get("EZX_POSTGRES_BIN_DIR", "/usr/local/bin/postgres"),
    arguments: ["-D", PGDATA],
    user: POSTGRES_USER,
    group: POSTGRES_USER,
  },
});
