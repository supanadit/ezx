// ezx bootstrap examples/bootstrap/demo.js
// Demonstrates the ezx host API: read env, provision files via the editor,
// and run a declarative process chain via the optional chain helper.
const { env, editor, chain, log } = require("ezx");

log.info("=== ezx demo script ===");

// Provision php.ini: set memory_limit from env, if provided.
const limit = env.get("PHP_MEMORY_LIMIT", "");
if (limit) {
	const e = editor.open("/tmp/ezx-php.ini");
	e.upsert(/^\s*memory_limit\s*=/, "memory_limit = " + limit);
	log.info("php.ini memory_limit = %s", limit);
}

// Provision a postgresql.conf from a POSTGRESQL_CONFIG_* env var, if set.
const sharedBuffers = env.get("POSTGRESQL_CONFIG_SHARED_BUFFERS", "");
if (sharedBuffers) {
	const e = editor.open("/tmp/ezx-postgresql.conf");
	e.upsert(/^\s*shared_buffers\s*=/, "shared_buffers = " + sharedBuffers);
	log.info("postgresql.conf shared_buffers = %s", sharedBuffers);
}

// Run a declarative process chain (optional high-level helper).
chain.run({
	roots: [
		{
			name: "hello-world-parent",
			process: {
				binaryPath: "/bin/sh",
				arguments: [
					"-c",
					"echo 'GREETING='$GREETING && echo 'PHP_MEMORY_LIMIT=${PHP_MEMORY_LIMIT:-FILTERED}' && cat /tmp/ezx-php.ini 2>/dev/null; cat /tmp/ezx-postgresql.conf 2>/dev/null; true",
				],
				environment: ["GREETING=Hello from EZX script"],
				filterEnv: ["PHP_MEMORY_LIMIT"],
			},
			children: [
				{
					name: "hello-world-child",
					process: { binaryPath: "/bin/sh", arguments: ["-c", "echo 'Hello from child process!'"] },
					needParentReady: true,
				},
			],
		},
	],
});

log.info("=== ezx demo script complete ===");
