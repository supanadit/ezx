// ezx bootstrap examples/bootstrap/oneshot-init.js
//
// Demonstrates oneshot (run-to-completion) services and the "init DAG → exec
// main" pattern: a chain of init steps (initdb, stanza-create) run to
// completion, and only when they all exit 0 does the main long-running app
// become PID 1 via `exec: true`.
//
// This is the declarative form of the imperative postgres.js entrypoint: each
// init step is a `oneshot: true` node whose dependents start only after it
// exits 0. The exec node may now depend on oneshot nodes — it fires only after
// all its oneshot deps succeed and no other long-running node is still
// supervised.
//
// To run against a real postgres image (e.g. docker run --entrypoint ezx ...):
//   go build -o ezx ./app
//   docker run --rm -e POSTGRES_PASSWORD=secret -v $(pwd)/ezx:/usr/local/bin/ezx \
//     -v $(pwd)/examples:/examples postgres:16 \
//     ezx bootstrap /examples/bootstrap/oneshot-init.js
const { chain } = require("ezx");

const PGDATA = "/var/lib/postgresql/data";
const POSTGRES_USER = "postgres";

chain.run({
  nodes: [
    // initdb runs to completion; stanza-create and postgres wait for it.
    {
      name: "initdb",
      oneshot: true,
      process: {
        binaryPath: "/usr/local/bin/initdb",
        arguments: ["--username=" + POSTGRES_USER, "--pwfile=/tmp/ezx-pwfile", PGDATA],
        user: POSTGRES_USER,
        group: POSTGRES_USER,
      },
    },
    // stanza-create runs only after initdb exits 0.
    {
      name: "stanza-create",
      oneshot: true,
      dependsOn: ["initdb"],
      process: {
        binaryPath: "/usr/bin/pgbackrest",
        arguments: ["stanza-create", "--stanza=main", "--pg1-path=" + PGDATA],
      },
    },
    // The main app becomes PID 1 only after both oneshot deps exit 0.
    {
      name: "postgres",
      exec: true,
      dependsOn: ["initdb", "stanza-create"],
      process: {
        binaryPath: "/usr/local/bin/postgres",
        arguments: ["-D", PGDATA],
        user: POSTGRES_USER,
        group: POSTGRES_USER,
      },
    },
  ],
});
