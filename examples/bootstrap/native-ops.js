// ezx bootstrap examples/bootstrap/native-ops.js
//
// Native stdlib-only replacements for the classic container toolbox. No curl,
// no cp -a, no sha256sum, no tar/gunzip, no sleep, no timeout — everything is
// implemented inside the ezx binary with Go's standard library.
//
//   curl       → net.http / net.download
//   cp -a      → fs.copy / fs.copyTree
//   sha256sum  → crypto.sha256 / crypto.sha256File
//   tar/gunzip → archive.create / archive.extract
//   sleep      → process.sleep(ns)
//   timeout    → process.run / process.capture { timeout: <ns> }
//
// Run:
//   ./ezx bootstrap examples/bootstrap/native-ops.js
const { fs, net, crypto, archive, process, env, log } = require("ezx");

// ---- sha256sum --------------------------------------------------------------
const payload = "hello, native operations";
const digest = crypto.sha256(payload);
log.info(`sha256 of %q: %s`, payload, digest);

// ---- cp -a: copy a tree preserving modes + symlinks --------------------------
const work = "/tmp/native-ops";
fs.removeAll(work);
fs.mkdirAll(work + "/app/conf", 0o755);
fs.write(work + "/app/main.sh", "#!/bin/sh\necho hi\n");
fs.chmod(work + "/app/main.sh", 0o755);
fs.write(work + "/app/conf/app.ini", "port=5432\n");
fs.symlink("main.sh", work + "/app/ln");
fs.copyTree(work + "/app", work + "/backup");           // recursive, -a style
fs.copy(work + "/app/conf/app.ini", work + "/single.ini");
log.info(`copied tree: ${fs.exists(work + "/backup/conf/app.ini")}`);

// ---- tar / gunzip -----------------------------------------------------------
archive.create(work + "/bundle.tar.gz",
  [work + "/app/main.sh", { source: work + "/app/conf/app.ini", dest: "custom.ini" }],
  { baseDir: work + "/app" });
fs.mkdirAll(work + "/unpack", 0o755);
archive.extract(work + "/bundle.tar.gz", work + "/unpack", { stripComponents: 0 });
log.info(`extracted: ${fs.exists(work + "/unpack/main.sh")}`);

// ---- curl: status fetch + sha256-verified download ---------------------------
// Point at any HTTP server you control. The optional verifySha256 makes
// net.download the curl -fsSL | sha256sum -c equivalent.
const url = env.get("EZX_NATIVE_OPS_URL", "https://example.com");
try {
  const res = net.http({ url, method: "GET", timeout: 5e9, check: true });
  log.info(`http GET %s → %d`, url, res.status);
} catch (e) {
  log.warn("http fetch failed (expected offline): %s", e.message);
}

// ---- sleep / timeout ---------------------------------------------------------
process.sleep(100e6); // 100 ms — sleep(1) replacement
try {
  process.run({
    process: { binaryPath: "/bin/sh", arguments: ["-c", "sleep 5"] },
    timeout: 300e6, // 300 ms — timeout(1) replacement
    check: true,
  });
} catch (e) {
  log.info(`timeout fired as expected: ${e.message}`);
}

log.info("native-ops.js done");