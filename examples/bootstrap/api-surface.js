// ezx bootstrap examples/bootstrap/api-surface.js
// Demonstrates the full script-visible API surface, filling the gaps the other
// examples leave uncovered: the advanced editor block/line methods, the
// remaining env/fs helpers, log.debug/log.enabled, health.setReady/ready, the
// api.put/api.delete verbs, and process.shell.
//
// Env to try:
//   EZX_HEALTH_ADDR=:8080     (required for api.* routes to register)
//   EXTRA_HOSTS=alpha,beta    (used by env.list)
//   SHUTDOWN_ON_IDLE=true     (used by env.isTruthy)
//
// The api.* routes are served while the script blocks in chain.run below; hit
// them from another terminal, e.g.:
//   curl -X PUT  http://localhost:8080/api/ping        -> { ok: true }
//   curl -X DELETE http://localhost:8080/api/ping      -> { ok: true }
//   curl -X POST http://localhost:8080/api/set-ready   -> { ready: true }
//   curl http://localhost:8080/readyz                  -> "ready"
const { env, editor, process, log, chain, fs, health, api, shell } = require("ezx");

log.info("=== api-surface example ===");

// ---------------------------------------------------------------------------
// env — the helpers not shown elsewhere: isTruthy, list, normalizeBool, all, has.
// ---------------------------------------------------------------------------
if (log.enabled("DEBUG")) {
  log.debug("env.all has %d entries", Object.keys(env.all()).length);
}
const extra = env.list("EXTRA_HOSTS", ","); // ["alpha","beta"]
log.info("EXTRA_HOSTS = %j", extra);

const idle = env.isTruthy("SHUTDOWN_ON_IDLE"); // honors shell default-true semantics
log.info("SHUTDOWN_ON_IDLE is truthy = %s", idle);
log.info("normalizeBool('ON') = %s", env.normalizeBool("ON"));
log.info("env.has('PATH') = %s", env.has("PATH"));

// ---------------------------------------------------------------------------
// editor — the block / line-level methods: readLines, writeLines, ensure,
// insertBefore, insertAfter, replaceBlock, setBlock, path.
// ---------------------------------------------------------------------------
const e = editor.open("/tmp/ezx-surface.conf");
e.replace("# empty\n"); // seed the file (creates it if missing)
e.ensure("base_url = http://localhost");
e.insertBefore(/^base_url/, "listen = 0.0.0.0"); // put before base_url
e.insertAfter(/^base_url/, "port = 8080"); // put after base_url
// Block ops: replace the block delimited by markers with new content.
e.replaceBlock(/^# BEGIN tls$/, /^# END tls$/, "# BEGIN tls\ntls = on\n# END tls");
e.setBlock(/^# BEGIN cache$/, /^# END cache$/, /^# BEGIN tls$/, "# BEGIN cache\ncache = 1GB\n# END cache");
const lines = e.readLines();
log.info("editor path=%s, %d lines", e.path(), lines.length);
log.info("--- /tmp/ezx-surface.conf ---\n%s", e.read());

// readLines/writeLines round-trip: drop blank lines and rewrite.
e.writeLines(e.readLines().filter((l) => l.trim() !== ""));

// ---------------------------------------------------------------------------
// fs — the helpers not shown elsewhere: mkdir, glob, realpath, stat, symlink,
// tempDir, chmodRecursive.
// ---------------------------------------------------------------------------
const dir = "/tmp/ezx-surface";
fs.mkdir(dir, 0o750);
fs.write(dir + "/a.conf", "a=1\n");
fs.write(dir + "/b.conf", "b=2\n");
fs.mkdir(dir + "/sub", 0o750);
fs.chmodRecursive(dir, 0o700); // recursive chmod
const matched = fs.glob(dir + "/*.conf");
log.info("fs.glob matched %d files: %j", matched.length, matched);
const real = fs.realpath(dir);
log.info("fs.realpath(%s) = %s", dir, real);
const st = fs.stat(dir + "/a.conf");
log.info("fs.stat: size=%d mode=%o isDir=%s", st.size, st.mode, st.isDir);
const tmpFile = fs.tempFile("/tmp", "surface-");
log.info("fs.tempFile = %s", tmpFile);
fs.symlink(dir + "/a.conf", "/tmp/ezx-surface-link.conf");
log.info("fs.symlink created, realpath(link) = %s", fs.realpath("/tmp/ezx-surface-link.conf"));
fs.remove(tmpFile);

// ---------------------------------------------------------------------------
// process.shell — the explicit /bin/sh -c escape hatch.
// ---------------------------------------------------------------------------
const code = process.shell("echo 'hello from shell' && test -f /tmp/ezx-surface.conf", {
  check: true,
});
log.info("process.shell exited %d", code);

// ---------------------------------------------------------------------------
// health + api — readiness flip and the put/delete verbs. Routes are served
// while the script blocks in chain.run below.
// ---------------------------------------------------------------------------
api.put("/api/ping", () => ({ ok: true }));
api.delete("/api/ping", () => ({ ok: true }));
api.post("/api/set-ready", () => {
  health.setReady(true);
  return { ready: health.ready() };
});

log.info("health.ready() before setReady = %s", health.ready());
health.setReady(true);
log.info("health.ready() after setReady  = %s", health.ready());

// ---------------------------------------------------------------------------
// chain — a short, self-terminating process so the script does not hang. While
// it runs, the api.* routes registered above are live on EZX_HEALTH_ADDR.
// ---------------------------------------------------------------------------
chain.run({
  roots: [
    {
      name: "surface-window",
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'api surface window open (hit the api.* routes now)'; sleep 5; echo 'closing'"],
      },
    },
  ],
});

log.info("=== api-surface example complete ===");
