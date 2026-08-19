// ezx bootstrap examples/bootstrap/postgres-health.js
//
// A variant of postgres.js in "server mode": instead of exec'ing postgres to
// become PID 1, ezx STAYS PID 1, supervises postgres as a child, and serves
// Kubernetes-style health endpoints (/livez, /readyz, /healthz) reflecting
// postgres readiness — no sidecar required.
//
// The HTTP server is process-wide (wired by the app when EZX_HEALTH_ADDR is
// set); this script only gates readiness via health.setReady. Declaratively,
// a node's `health.readyProbe` also drives /readyz through the orchestrator.
const { env, fs, process, chain, log } = require("ezx");

const PGDATA = env.get("PGDATA", "/var/lib/postgresql/data");
const POSTGRES_USER = env.get("POSTGRES_USER", "postgres");
const POSTGRES_DB = env.get("POSTGRES_DB", POSTGRES_USER);
const POSTGRES_PASSWORD = env.get("POSTGRES_PASSWORD");
const POSTGRES_HOST_AUTH_METHOD = env.get("POSTGRES_HOST_AUTH_METHOD");

if (!POSTGRES_PASSWORD && POSTGRES_HOST_AUTH_METHOD !== "trust") {
  throw new Error("POSTGRES_PASSWORD (or POSTGRES_HOST_AUTH_METHOD=trust) is required");
}

fs.mkdirAll(PGDATA, 0o700);

// Declarative: a single supervised root with a Health readyProbe. The presence
// of Health prevents the lone-leaf exec default, so ezx stays PID 1. The
// orchestrator polls readyProbe and drives /readyz (server owned by the app).
chain.run({
  roots: [
    {
      name: "postgres",
      process: {
        binaryPath: env.get("EZX_POSTGRES_BIN", "/usr/local/bin/postgres"),
        arguments: ["-D", PGDATA],
        user: POSTGRES_USER,
        group: POSTGRES_USER,
        environment: ["PGPORT=" + env.get("PGPORT", "5432")],
      },
      // Health readyProbe keeps ezx alive and gates /readyz.
      health: {
        readyProbe: { type: "tcp", tcp: { host: "127.0.0.1", port: parseInt(env.get("PGPORT", "5432"), 10) } },
      },
      // Relay standard signals to postgres (log rotation, reload, etc.). The
      // shutdown signal defaults to SIGTERM (JS strings can't map to os.Signal).
      forwardSignals: ["SIGTERM", "SIGINT", "SIGHUP", "SIGUSR1", "SIGUSR2"],
      shutdown: { timeout: 30 * 1e9, forceKill: true },
    },
  ],
});

log.info("postgres-health example complete");
