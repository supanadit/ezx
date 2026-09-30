// ezx bootstrap examples/log-rotation-app.js
// Demonstrates rotating log files that the supervised process opens itself —
// the case log.stdout/log.filePath cannot cover, because ezx does not own the
// descriptor. (Traefik, Apache httpd and Nginx all write their access/error
// logs this way and document a reopen signal: USR1 for Traefik/Nginx, USR1
// (graceful restart) for Apache.)
//
// How it works here: /bin/sh opens the log file once with `exec 3>>..."$LOG"`
// (a persistent descriptor, exactly like a real server), writes to fd 3, and
// traps USR1 to close and reopen fd 3. ezx watches the log directory, renames
// the file when a write crosses maxBytes, and sends USR1 once per pass — so the
// app reopens a fresh file instead of writing to the unlinked inode.
//
// After the run, inspect the log dir:
//   ls -la /tmp/ezx-app-rotate
//   head -2 /tmp/ezx-app-rotate/app.log.1
//   # with compress: true, archives are app.log.1.gz
const { chain } = require("ezx");

const dir = "/tmp/ezx-app-rotate";

chain.run({
  // Keep ezx as PID 1 so it can supervise and signal; without this a lone node
  // would be exec'd and rotation would have nothing to signal.
  execDefault: false,
  nodes: [
    {
      name: "writer",
      process: {
        binaryPath: "/bin/sh",
        arguments: [
          "-c",
          `
          LOG="${dir}/app.log"
          mkdir -p "$(dirname "$LOG")"
          reopen() { exec 3>&-; exec 3>>"$LOG"; }
          trap reopen USR1
          reopen
          i=0
          while [ $i -lt 400 ]; do
            i=$((i+1))
            echo "line $i 0123456789012345678901234567890" >&3
            sleep 0.02
          done
          exec 3>&-
          `,
        ],
      },

      // Size-driven rotation of files the app owns. No cron, no shell script:
      // ezx watches the directory and rotates on write, then signals USR1 once
      // for the whole pass so the app reopens its descriptor.
      logRotate: {
        signal: "USR1", // reopen handshake (Apache/Nginx/Traefik style)
        maxBackups: 3, // keep app.log + .1 + .2 + .3
        compress: false, // set true to gzip each archive (app.log.1.gz)
        exclude: ["*.gz", "*.1"], // never rotate our own archives
        files: [{ include: dir + "/*.log", maxBytes: 1024 }],
        // Optional safety net if change events are unreliable (e.g. a network
        // mount written from another host): interval: 5e9,  // 5s poll
      },
    },
  ],
});
