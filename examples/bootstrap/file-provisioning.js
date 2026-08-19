// ezx bootstrap examples/bootstrap/file-provisioning.js
// Declarative env-to-file provisioning, mirroring the FileProvision examples
// from the old in-code demo: conditional removal, env-prefix enumeration into
// config keys, name transforms, and value formatting.
//
// Env to try:
//   NODE_ROLE=primary
//   POSTGRESQL_CONFIG_SHARED_BUFFERS=256MB POSTGRESQL_CONFIG_MAX_CONNECTIONS=200
//   KAFKA_CONFIG_LOG_RETENTION_MS=60000
const { chain, log } = require("ezx");

chain.run({
  roots: [
    {
      name: "file-provider",
      process: {
        binaryPath: "/bin/sh",
        arguments: [
          "-c",
          "echo '--- pgbackrest.conf ---'; cat /tmp/ex-pgbackrest.conf 2>/dev/null; echo '--- postgresql.conf ---'; cat /tmp/ex-postgresql.conf 2>/dev/null; echo '--- kafka.properties ---'; cat /tmp/ex-kafka.properties 2>/dev/null; exit 0",
        ],
        // Strip config vars consumed into files so they don't leak to the process.
        filterEnvPattern: ["^POSTGRESQL_CONFIG_", "^KAFKA_CONFIG_"],
        workingDir: "/tmp",
      },
      files: [
        // Conditional: only when NODE_ROLE=primary, remove pg2-* and set backup-standby=n.
        {
          path: "/tmp/ex-pgbackrest.conf",
          permission: 420, // 0644
          when: { name: "NODE_ROLE", value: "primary" },
          operations: [
            { type: "remove", pattern: "^pg2-" },
            { type: "set-property", pattern: "^backup-standby=", value: "backup-standby=n" },
          ],
        },
        // Env-prefix enumeration: POSTGRESQL_CONFIG_* -> lowercased key = value.
        {
          path: "/tmp/ex-postgresql.conf",
          operations: [
            {
              type: "set-property",
              fromEnvPattern: "^POSTGRESQL_CONFIG_(.+)$",
              nameTransform: "lower",
              pattern: "^\\s*#?\\s*${name}\\s*=.*",
              value: "${name} = ${value}",
              valueFormat: "auto",
            },
          ],
        },
        // Kafka: POSTGRESQL_CONFIG_* style -> snake-to-dot key=value.
        {
          path: "/tmp/ex-kafka.properties",
          operations: [
            {
              type: "set-property",
              fromEnvPattern: "^KAFKA_CONFIG_(.+)$",
              nameTransform: "snake-to-dot",
              pattern: "^${name}=.*",
              value: "${name}=${value}",
            },
          ],
        },
      ],
    },
  ],
});

log.info("file-provisioning example complete");
