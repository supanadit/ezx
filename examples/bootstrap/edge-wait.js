// ezx bootstrap examples/bootstrap/edge-wait.js
//
// Demonstrates per-edge wait modes with `dependsOnEdges`. Each dependency edge
// can independently specify when a dependent may start:
//
//   - waitFor: "started"  (default) — wait for the dep to start
//   - waitFor: "ready"    — wait for the dep's readiness phase to complete
//   - waitFor: "exit"     — wait for the dep to permanently exit 0 (a oneshot's
//                           success gate)
//
// Topology:
//   postgres ──"ready"──► pgbouncer ──┐
//   pgbackrest ──"started"────────────┤──► backup (waits for pgbouncer ready
//   stanza-create (oneshot) ──"exit"──┘      AND pgbackrest started AND the
//                                            oneshot to exit 0)
//
// The legacy `dependsOn: ["a"]` + `needParentReady: true` form is exactly
// `dependsOnEdges: [{ name: "a", waitFor: "ready" }]` — it still works.
const { chain, log } = require("ezx");

chain.run({
  nodes: [
    {
      name: "postgres",
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'postgresql started'; sleep 1; echo 'postgresql stopping'; exit 0"],
        workingDir: "/tmp",
      },
    },
    // A long-running sidecar that backup only waits to *start*, not be ready.
    {
      name: "pgbackrest",
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'pgbackrest started'; sleep 1; echo 'pgbackrest stopping'; exit 0"],
        workingDir: "/tmp",
      },
    },
    // A oneshot init step; backup waits for it to exit 0 before starting.
    {
      name: "stanza-create",
      oneshot: true,
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'stanza created'; exit 0"],
        workingDir: "/tmp",
      },
    },
    // pgbouncer waits only for postgres to be READY.
    {
      name: "pgbouncer",
      dependsOnEdges: [{ name: "postgres", waitFor: "ready" }],
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'pgbouncer started'; sleep 1; echo 'pgbouncer stopping'; exit 0"],
        workingDir: "/tmp",
      },
    },
    // backup waits for pgbouncer ready AND pgbackrest started AND the oneshot
    // to exit 0 — three different wait modes on three edges.
    {
      name: "backup",
      dependsOnEdges: [
        { name: "pgbouncer", waitFor: "ready" },
        { name: "pgbackrest", waitFor: "started" },
        { name: "stanza-create", waitFor: "exit" },
      ],
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'backup started'; sleep 1; echo 'backup stopping'; exit 0"],
        workingDir: "/tmp",
      },
    },
  ],
});

log.info("per-edge wait modes example complete");
