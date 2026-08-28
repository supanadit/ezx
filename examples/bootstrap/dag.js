// ezx bootstrap examples/bootstrap/dag.js
// A flat dependency-graph (DAG) with fan-in and a shared dependency, using the
// canonical `nodes` + `dependsOn` form.
//
// Topology:
//   postgres ──► pgbouncer ──┐
//                  │         ├──► backup (fan-in: waits for BOTH pgbouncer AND pgbackrest ready)
//   pgbackrest ──────────────┘
//   (postgres is a shared dependency of pgbouncer and pgbackrest)
//
// Ordering:
//   - pgbouncer and pgbackrest start after postgres is ready
//   - backup starts only after BOTH pgbouncer and pgbackrest are ready
//     (fan-in / needParentReady applied to all dependencies)
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
    {
      name: "pgbouncer",
      dependsOn: ["postgres"],
      needParentReady: true,
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'pgbouncer started'; sleep 1; echo 'pgbouncer stopping'; exit 0"],
        workingDir: "/tmp",
      },
    },
    {
      name: "pgbackrest",
      dependsOn: ["postgres"],
      needParentReady: true,
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'pgbackrest started'; sleep 1; echo 'pgbackrest stopping'; exit 0"],
        workingDir: "/tmp",
      },
    },
    {
      name: "backup",
      dependsOn: ["pgbouncer", "pgbackrest"],
      needParentReady: true,
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'backup started'; sleep 1; echo 'backup stopping'; exit 0"],
        workingDir: "/tmp",
      },
    },
  ],
});

log.info("dependency-graph example complete");
