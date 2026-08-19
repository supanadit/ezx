// ezx bootstrap examples/bootstrap/restart.js
// Demonstrates the restart policy and graceful shutdown config on a ProcessNode.
//
// Run it with the default (SIGTERM, 30s timeout, force-kill). The process exits
// non-zero repeatedly and is restarted up to MaxRetries times with Backoff.
const { chain, log } = require("ezx");

// Go time.Duration values are nanoseconds. 200ms expressed in ns:
const ms = 1e6;
const backoff = 200 * ms;

chain.run({
  roots: [
    {
      name: "flaky",
      process: {
        binaryPath: "/bin/sh",
        arguments: ["-c", "echo 'flaky attempt'; exit 7"],
        workingDir: "/tmp",
      },
      // Restart on failure, up to 3 retries, 200ms apart.
      restart: {
        mode: "on-failure",
        maxRetries: 3,
        backoff: backoff,
      },
      // Graceful shutdown: defaults to SIGTERM with the given timeout and
      // force-kill behavior. Timeout is in nanoseconds (5s here).
      shutdown: {
        timeout: 5 * 1e9,
        forceKill: true,
      },
    },
  ],
});

log.info("restart example complete");
