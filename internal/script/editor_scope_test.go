package script

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/supanadit/ezx/internal/repository/system"
)

// writeFile writes content to dir/name and returns the path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestEditorPropertyInPlace verifies Property replaces a directive where it
// lives, preserving position (the httpd.conf bug: set-property hoisted it to EOF).
func TestEditorPropertyInPlace(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "httpd.conf", strings.Join([]string{
		"ServerRoot \"/usr/local/apache2\"",
		"DocumentRoot \"/usr/local/apache2/htdocs\"",
		"User daemon",
		"<Directory \"/usr/local/apache2/htdocs\">",
		"    AllowOverride None",
		"</Directory>",
		"",
	}, "\n"))

	ed := NewEditorModule(system.NewAdapter()).Open(target)
	if err := ed.Property("DocumentRoot", `"/var/www/html"`); err != nil {
		t.Fatal(err)
	}
	if err := ed.Property("User", "www-data"); err != nil {
		t.Fatal(err)
	}

	got := ed.Read()
	wantOrder := []string{
		"ServerRoot \"/usr/local/apache2\"",
		`DocumentRoot "/var/www/html"`,
		"User www-data",
		"<Directory \"/usr/local/apache2/htdocs\">",
	}
	idx := -1
	for _, w := range wantOrder {
		i := strings.Index(got, w)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", w, got)
		}
		if i < idx {
			t.Fatalf("line %q moved out of order:\n%s", w, got)
		}
		idx = i
	}
	if strings.Count(got, "User") != 1 {
		t.Fatalf("User directive duplicated:\n%s", got)
	}
}

// TestEditorPropertyAppendsWhenAbsent verifies Property appends when no line
// matches (create-if-missing).
func TestEditorPropertyAppendsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "conf", "a 1\n")
	ed := NewEditorModule(system.NewAdapter()).Open(target)
	if err := ed.Property("b", "2"); err != nil {
		t.Fatal(err)
	}
	if got := ed.Read(); got != "a 1\nb 2\n" {
		t.Fatalf("got %q", got)
	}
}

// TestEditorWrapSection verifies wrap() creates a nested scope and that
// property/add land inside it, and that the whole thing is idempotent.
func TestEditorWrapSection(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "httpd.conf", "ServerRoot \"/usr/local/apache2\"\n")

	ed := NewEditorModule(system.NewAdapter()).Open(target)
	rewrite, err := ed.Wrap("<IfModule mod_rewrite.c>", "</IfModule>")
	if err != nil {
		t.Fatal(err)
	}
	if err := rewrite.Property("RewriteEngine", "on"); err != nil {
		t.Fatal(err)
	}
	if err := rewrite.Property("RewriteBase", "/"); err != nil {
		t.Fatal(err)
	}
	if err := rewrite.Add("RewriteCond", "%{REQUEST_FILENAME} !-f"); err != nil {
		t.Fatal(err)
	}
	if err := rewrite.Add("RewriteCond", "%{REQUEST_FILENAME} !-d"); err != nil {
		t.Fatal(err)
	}

	got := ed.Read()
	for _, want := range []string{
		"<IfModule mod_rewrite.c>",
		"RewriteEngine on",
		"RewriteBase /",
		"RewriteCond %{REQUEST_FILENAME} !-f",
		"RewriteCond %{REQUEST_FILENAME} !-d",
		"</IfModule>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}

	// Re-run: idempotent.
	rewrite2, _ := ed.Wrap("<IfModule mod_rewrite.c>", "</IfModule>")
	_ = rewrite2.Property("RewriteEngine", "on")
	if c := strings.Count(ed.Read(), "<IfModule mod_rewrite.c>"); c != 1 {
		t.Fatalf("wrap duplicated block (%d):\n%s", c, ed.Read())
	}
}

// TestEditorSectionMarkers verifies section() writes BEGIN/END markers and that
// raw() fills the body, idempotently.
func TestEditorSectionMarkers(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, ".htaccess", "")

	ed := NewEditorModule(system.NewAdapter()).Open(target)
	wp, err := ed.SectionNamed("wordpress", "# BEGIN WordPress", "# END WordPress")
	if err != nil {
		t.Fatal(err)
	}
	if err := wp.Raw("<IfModule mod_rewrite.c>\n    RewriteEngine On\n</IfModule>"); err != nil {
		t.Fatal(err)
	}

	got := ed.Read()
	for _, want := range []string{"# BEGIN WordPress", "RewriteEngine On", "# END WordPress"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}

	// Re-run section + raw: body replaced, markers not duplicated.
	wp2, _ := ed.SectionNamed("wordpress", "# BEGIN WordPress", "# END WordPress")
	_ = wp2.Raw("<IfModule mod_rewrite.c>\n    RewriteEngine Off\n</IfModule>")
	final := ed.Read()
	if c := strings.Count(final, "# BEGIN WordPress"); c != 1 {
		t.Fatalf("section markers duplicated (%d):\n%s", c, final)
	}
	if strings.Contains(final, "RewriteEngine On") {
		t.Fatalf("section body not replaced:\n%s", final)
	}
	if !strings.Contains(final, "RewriteEngine Off") {
		t.Fatalf("section body not updated:\n%s", final)
	}
}

// TestEditorDefaultSectionMarkers verifies section(name) uses the "#" comment
// default.
func TestEditorDefaultSectionMarkers(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "conf", "")
	ed := NewEditorModule(system.NewAdapter()).Open(target)
	s, err := ed.Section("mydb")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Property("port", "5432")
	got := ed.Read()
	for _, want := range []string{"# BEGIN mydb", "# END mydb", "port 5432"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// TestEditorSeparatorOption verifies the per-file separator profile.
func TestEditorSeparatorOption(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "postgresql.conf", "listen_addresses = '*'\n")
	ed := NewEditorModule(system.NewAdapter()).Open(target, EditorOptions{Separator: " = "})
	if err := ed.Property("port", "5432"); err != nil {
		t.Fatal(err)
	}
	// Value is verbatim: quoting is the caller's concern, so pass "'*'".
	if err := ed.Property("listen_addresses", "'*'"); err != nil {
		t.Fatal(err)
	}
	got := ed.Read()
	if !strings.Contains(got, "port = 5432") {
		t.Fatalf("separator not applied:\n%s", got)
	}
	if c := strings.Count(got, "listen_addresses = '*'"); c != 1 {
		t.Fatalf("listen_addresses not replaced in place (%d):\n%s", c, got)
	}
}

// TestEditorKeyAnchoring verifies `User` does not match `UserDir`.
func TestEditorKeyAnchoring(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "conf", "UserDir disabled\nUser daemon\n")
	ed := NewEditorModule(system.NewAdapter()).Open(target)
	if err := ed.Property("User", "www-data"); err != nil {
		t.Fatal(err)
	}
	got := ed.Read()
	if !strings.Contains(got, "UserDir disabled") {
		t.Fatalf("UserDir clobbered:\n%s", got)
	}
	if !strings.Contains(got, "User www-data") {
		t.Fatalf("User not set:\n%s", got)
	}
}

// TestEditorLineInPlace verifies Line replaces a structural single line in
// place (no key/value shape), e.g. an Apache <Directory "X"> opener.
func TestEditorLineInPlace(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, dir, "httpd.conf", strings.Join([]string{
		"DocumentRoot \"/usr/local/apache2/htdocs\"",
		"<Directory \"/usr/local/apache2/htdocs\">",
		"    AllowOverride None",
		"</Directory>",
		"",
	}, "\n"))
	ed := NewEditorModule(system.NewAdapter()).Open(target)
	if err := ed.Line(`^\s*<Directory\s+"`, `<Directory "/var/www/html">`); err != nil {
		t.Fatal(err)
	}
	if err := ed.Line(`^\s*AllowOverride\s+`, "AllowOverride All"); err != nil {
		t.Fatal(err)
	}
	got := ed.Read()
	want := "DocumentRoot \"/usr/local/apache2/htdocs\"\n<Directory \"/var/www/html\">\nAllowOverride All\n</Directory>\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestEditorFragment verifies fragment() reads a source file, fences it, places
// it at the anchor, is idempotent, and removes when path is empty.
func TestEditorFragment(t *testing.T) {
	dir := t.TempDir()
	php := writeFile(t, dir, "https.php", "<?php\nif ( $proto === 'https' ) {\n    $_SERVER['HTTPS'] = 'on';\n}\n")
	cfg := writeFile(t, dir, "wp-config.php", "<?php\n// body\n")

	ed := NewEditorModule(system.NewAdapter()).Open(cfg, EditorOptions{Comment: "//"})
	if err := ed.Fragment("https", php, FragmentOptions{After: `^<\?php`}); err != nil {
		t.Fatal(err)
	}
	got := ed.Read()
	for _, want := range []string{"// BEGIN ezx:https", "$_SERVER['HTTPS'] = 'on';", "// END ezx:https"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	// The leading <?php of the fragment is stripped (it is included into an
	// already-open PHP file), so only the file's own <?php remains.
	if c := strings.Count(got, "<?php"); c != 1 {
		t.Fatalf("fragment <?php not stripped (%d):\n%s", c, got)
	}
	// The fragment is placed right after <?php.
	if strings.Index(got, "// BEGIN ezx:https") < strings.Index(got, "<?php") {
		t.Fatalf("fragment not after <?php:\n%s", got)
	}

	// Idempotent.
	_ = ed.Fragment("https", php, FragmentOptions{After: `^<\?php`})
	if c := strings.Count(ed.Read(), "// BEGIN ezx:https"); c != 1 {
		t.Fatalf("fragment duplicated (%d):\n%s", c, ed.Read())
	}

	// Remove.
	_ = ed.Fragment("https", "")
	if strings.Contains(ed.Read(), "ezx:https") {
		t.Fatalf("fragment not removed:\n%s", ed.Read())
	}
}

// TestEditorDefine verifies PHP define() with type-aware quoting.
func TestEditorDefine(t *testing.T) {
	dir := t.TempDir()
	cfg := writeFile(t, dir, "wp-config.php", "<?php\ndefine('WP_DEBUG', false);\n")
	ed := NewEditorModule(system.NewAdapter()).Open(cfg)

	_ = ed.Define("WP_DEBUG", "true")
	_ = ed.Define("WP_MEMORY_LIMIT", "256M")
	_ = ed.Define("WP_REDIS_CONFIG", "[ 'host' => 'redis' ]")

	got := ed.Read()
	for _, want := range []string{
		"define('WP_DEBUG', true);",
		"define('WP_MEMORY_LIMIT', '256M');",
		"define('WP_REDIS_CONFIG', [ 'host' => 'redis' ]);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if c := strings.Count(got, "define('WP_DEBUG'"); c != 1 {
		t.Fatalf("WP_DEBUG duplicated (%d):\n%s", c, got)
	}
}
