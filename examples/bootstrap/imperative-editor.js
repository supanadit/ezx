// ezx bootstrap examples/bootstrap/imperative-editor.js
// Imperative file processing from JS, mirroring the Go ProcessFunc callback
// from the old in-code demo. Here the full logic is expressed in the script:
// read env, conditionally upsert config keys, and validate before writing.
//
// Env to try:
//   PHP_MEMORY_LIMIT=512M
//   PHP_MAX_EXECUTION_TIME=300
const { env, editor, log } = require("ezx");

log.info("=== imperative editor example ===");

// Replicates the old ProcessFunc: upsert php.ini memory_limit from env.
const phpIni = editor.open("/tmp/ex-php.ini");

const limit = env.get("PHP_MEMORY_LIMIT", "");
if (limit) {
  // Simple unit validation, like the old code checked before writing.
  if (!/^\d+[kKmMgG]?$/.test(limit)) {
    throw new Error("PHP_MEMORY_LIMIT must be a size like 512M, got " + limit);
  }
  phpIni.upsert(/^\s*memory_limit\s*=/, "memory_limit = " + limit);
  log.info("php.ini memory_limit = %s", limit);
}

// A second, imperative upsert from a different env var.
const maxExec = env.get("PHP_MAX_EXECUTION_TIME", "");
if (maxExec) {
  phpIni.upsert(/^\s*max_execution_time\s*=/, "max_execution_time = " + maxExec);
  log.info("php.ini max_execution_time = %s", maxExec);
}

// Read back what was written to confirm.
const contents = phpIni.read();
log.info("--- ex-php.ini ---\n%s", contents);
log.info("=== imperative editor example complete ===");
