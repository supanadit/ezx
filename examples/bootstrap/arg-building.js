// ezx bootstrap examples/bootstrap/arg-building.js
// Declarative env-to-CLI-argument building, mirroring the ArgOperation examples
// from the old in-code demo: if-set values, if-truthy bare flags, list splits,
// pattern enumeration with name transforms, and whitespace pass-through.
//
// Env to try:
//   PROMETHEUS_WEB_CONFIG_FILE=/etc/prometheus/prometheus.yml
//   PROMETHEUS_ENABLE_WEB_LIFECYCLE=true
//   PROMETHEUS_ENABLE_NATIVE_HISTOGRAM=true
//   THANOS_QUERY_STORE_ADDRESSES=store1:10901,store2:10901
//   THANOS_RECEIVE_LABELS_REGION=ap-southeast
//   THANOS_EXTRA_ARGS="--query.max-concurrency 10"
const { chain, log } = require("ezx");

chain.run({
  roots: [
    {
      name: "arg-builder",
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", 'for a in "$@"; do echo "ARG: $a"; done', "prometheus"],
        argOperations: [
          // if-set value (only when PROMETHEUS_WEB_CONFIG_FILE is set)
          { flag: "--web.config.file", fromEnv: "PROMETHEUS_WEB_CONFIG_FILE" },
          // if-truthy bare flag
          {
            when: { name: "PROMETHEUS_ENABLE_WEB_LIFECYCLE", value: "true" },
            flag: "--web.enable-lifecycle",
            format: "bare-flag",
          },
          // if-truthy feature flag with literal value
          {
            when: { name: "PROMETHEUS_ENABLE_NATIVE_HISTOGRAM", value: "true" },
            flag: "--enable-feature",
            value: "native-histograms",
          },
          // comma-split list -> one --endpoint per element
          { flag: "--endpoint", fromEnv: "THANOS_QUERY_STORE_ADDRESSES", split: "," },
          // pattern-enum with name transform: THANOS_RECEIVE_LABELS_REGION -> region="ap-southeast"
          {
            flag: "--label",
            fromEnvPattern: "^THANOS_RECEIVE_LABELS_(.+)$",
            value: '${name}="${value}"',
            nameTransform: "lower",
          },
          // whitespace-split pass-through (raw)
          { fromEnv: "THANOS_EXTRA_ARGS", split: " ", format: "raw" },
        ],
        workingDir: "/tmp",
      },
    },
  ],
});

log.info("arg-building example complete");
