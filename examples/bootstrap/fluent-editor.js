// ezx bootstrap examples/bootstrap/fluent-editor.js
// The fluent, human-readable editor. It replaces hand-assembled config text
// (`lines.push(...).join("\n")`, manual `sed` regexes, `.htaccess`
// splice/indexOf dances) with verbs that mirror the file's structure.
//
//   property  — set a `key<sep>value` directive, in place
//   add       — append a repeatable directive if not already present (idempotent)
//   repeat    — append a repeatable directive even if identical (rare)
//   wrap      — a structural block: <IfModule ...> ... </IfModule>
//   section   — a comment-delimited managed region: # BEGIN name / # END name
//   raw       — verbatim (multi-line) content
//
// Every verb writes immediately and is idempotent, so re-running converges.
const { editor, log } = require("ezx");

const conf = editor.open("/tmp/ex-fluent-httpd.conf");
conf.replace('ServerRoot "/usr/local/apache2"\n');

// Directives: set in place, wherever they live.
conf.property("DocumentRoot", '"/var/www/html"');
conf.property("User", "www-data");
conf.property("DirectoryIndex", "index.php index.html");

// A nested structural block. The handle scopes every call inside it.
const rewrite = conf.wrap("<IfModule mod_rewrite.c>", "</IfModule>");
rewrite.property("RewriteEngine", "on");
rewrite.property("RewriteBase", "/");
rewrite.add("RewriteCond", "%{REQUEST_FILENAME} !-f");
rewrite.add("RewriteCond", "%{REQUEST_FILENAME} !-d");
rewrite.property("RewriteRule", ". /index.php [L]");

// A comment-delimited managed section (markers written by ezx). reset() makes
// the region fully owned, so repeatable directives do not accumulate.
const wp = conf.sectionNamed("wordpress", "# BEGIN WordPress", "# END WordPress");
wp.reset();
wp.property("RewriteEngine", "On");
wp.add("RewriteCond", "%{REQUEST_FILENAME} !-f");
wp.add("RewriteRule", ". /index.php [L]");

// Application source (PHP) ships as a real .php file and is included by path —
// never written as a JS string. fragment() fences it so it is replaceable and
// removable; define() writes a PHP constant with type-aware quoting.
const cfg = editor.open("/tmp/ex-fluent-wp-config.php", { comment: "//" });
cfg.replace("<?php\n");
cfg.define("WP_DEBUG", "false");
cfg.define("WP_MEMORY_LIMIT", "256M");
cfg.fragment("loopback", "examples/php/loopback.php", { after: "^<\\?php" });

// A per-file separator profile (postgresql.conf uses " = ").
const pg = editor.open("/tmp/ex-fluent-postgresql.conf", { separator: " = " });
pg.property("listen_addresses", "'*'");
pg.property("port", "5432");

log.info("--- httpd.conf ---\n%s", conf.read());
log.info("--- wp-config.php ---\n%s", cfg.read());
log.info("--- postgresql.conf ---\n%s", pg.read());
