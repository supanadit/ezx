// ezx bootstrap examples/bootstrap/env-filtering.js
// Demonstrates blocking/filtering environment variables passed to a spawned
// process, mirroring the FilterEnv / FilterEnvPattern fields from the old
// in-code demo. Secrets and consumed config vars are stripped before the
// process runs; additive Environment entries survive.
//
// Env to try:
//   MINIO_ROOT_PASSWORD=supersecret
//   POSTGRESQL_CONFIG_SHARED_BUFFERS=256MB
const { chain, log } = require("ezx");

chain.run({
  roots: [
    {
      name: "env-filtered",
      process: {
        binaryPath: "/bin/sh",
        // Double quotes so the shell expands the env vars inside the -c string.
        arguments: [
          "-c",
          'echo "SECRET=[$MINIO_ROOT_PASSWORD]"; echo "BUFFERS=[$POSTGRESQL_CONFIG_SHARED_BUFFERS]"; echo "KEEP=[$KEEP]"',
        ],
        // Additive env entries survive filtering and are appended last.
        environment: ["KEEP=kept"],
        // Exact filter: strip a secret by name.
        filterEnv: ["MINIO_ROOT_PASSWORD"],
        // Pattern filter: strip all config vars consumed into files.
        filterEnvPattern: ["^POSTGRESQL_CONFIG_"],
        workingDir: "/tmp",
      },
    },
  ],
});

log.info("env-filtering example complete");
