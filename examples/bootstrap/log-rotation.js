// ezx bootstrap examples/bootstrap/log-rotation.js
// Demonstrates per-service file-backed logging with size-based rotation.
//
// Each node routes its stdout to a rotating file. maxBytes is set tiny (100B)
// so a short run visibly rotates: the active file fills, then shifts to .1, .2,
// and drops beyond maxBackups. Both streams can share one filePath (one shared
// writer, interleaved coherently).
//
// After the run, inspect the log dir:
//   ls -la /tmp/ezx-log-rotation
//   cat /tmp/ezx-log-rotation/app.log
//   cat /tmp/ezx-log-rotation/app.log.1
const { chain } = require("ezx");

const logDir = "/tmp/ezx-log-rotation";

chain.run({
  nodes: [
    {
      name: "noisy",
      process: {
        binaryPath: "/bin/sh",
        arguments: [
          "-c",
          "for i in $(seq 1 20); do echo \"noisy line $i 012345678901234567890123456789\"; done",
        ],
        log: {
          stdout: "file",
          filePath: logDir + "/noisy.log",
          maxBytes: 100, // rotate every ~100 bytes
          maxBackups: 2, // keep noisy.log + .1 + .2
        },
      },
    },
    {
      name: "both-streams",
      process: {
        binaryPath: "/bin/sh",
        arguments: [
          "-c",
          "for i in $(seq 1 10); do echo \"out $i 012345678901234567890123456789\"; echo \"err $i 012345678901234567890123456789\" >&2; done",
        ],
        log: {
          stdout: "file",
          stderr: "file",
          filePath: logDir + "/both.log", // shared path → one writer
          maxBytes: 100,
          maxBackups: 3,
        },
      },
    },
  ],
});
