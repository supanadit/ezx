// ezx bootstrap examples/bootstrap/process-tree.js
// A nested process dependency tree with parent-readiness gating, mirroring the
// postgres → pgbouncer → pgpool + etcd example from the old in-code demo.
//
// Ordering:
//   - etcd and pgbouncer/pgpool (children of postgres) start after postgres
//   - pgpool starts only after pgbouncer is ready (NeedParentReady)
const { chain, log } = require("ezx");

chain.run({
  roots: [
    {
      name: "postgresql",
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'postgresql started'; sleep 1; echo 'postgresql stopping'; exit 0"],
        workingDir: "/tmp",
      },
      children: [
        {
          name: "pgbouncer",
          process: {
            binaryPath: "/bin/sh",
            arguments: ["-c", "echo 'pgbouncer started'; sleep 1; echo 'pgbouncer stopping'; exit 0"],
            workingDir: "/tmp",
          },
          needParentReady: true,
          children: [
            {
              name: "pgpool",
              process: {
                binaryPath: "/bin/sh",
                arguments: ["-c", "echo 'pgpool started'; sleep 1; echo 'pgpool stopping'; exit 0"],
                workingDir: "/tmp",
              },
              needParentReady: true,
            },
          ],
        },
        {
          name: "etcd",
          process: {
            binaryPath: "/bin/sh",
            arguments: ["-c", "echo 'etcd started'; sleep 1; echo 'etcd stopping'; exit 0"],
            workingDir: "/tmp",
          },
        },
      ],
    },
  ],
});

log.info("process-tree example complete");
