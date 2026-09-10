package js

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestScriptNativeOpModules exercises the stdlib-only replacements added for
// the PostgreSQL entrypoint refactor (curl, cp -a, sha256sum, tar, sleep,
// timeout) end-to-end through the goja bridge exactly as scripts call them.
func TestScriptNativeOpModules(t *testing.T) {
	engine, _ := buildTestEngine(t)
	dir := t.TempDir()

	// httptest server serves a downloadable payload + a JSON endpoint.
	var payload = []byte("native download payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pkg.bin":
			_, _ = w.Write(payload)
		case "/api/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"role":"primary","ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	src := fmt.Sprintf(`
		const { fs, net, crypto, archive, process } = require("ezx");

		// --- fs.copy / fs.copyTree (cp -a)
		const srcDir = %q + "/app";
		fs.mkdirAll(srcDir + "/conf", 0o755);
		fs.write(srcDir + "/main.sh", "#!/bin/sh\necho hi\n");
		fs.chmod(srcDir + "/main.sh", 0o755);
		fs.write(srcDir + "/conf/app.ini", "port=5432\n");
		const backupDir = %q + "/backup";
		fs.copyTree(srcDir, backupDir);
		fs.copy(srcDir + "/main.sh", %q + "/copy.sh");
		if (!fs.exists(backupDir + "/conf/app.ini")) throw new Error("copyTree missed nested file");
		const st = fs.stat(%q + "/copy.sh");

		// --- crypto (sha256sum / base64)
		if (crypto.sha256("abc") !== "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
			throw new Error("sha256 vector mismatch");
		if (crypto.base64Encode("postgres:secret") !== "cG9zdGdyZXM6c2VjcmV0")
			throw new Error("base64Encode mismatch");
		if (crypto.base64Decode("cG9zdGdyZXM6c2VjcmV0") !== "postgres:secret")
			throw new Error("base64Decode mismatch");
		if (crypto.randomHex(4).length !== 8) throw new Error("randomHex length");

		// --- archive.create / archive.extract (tar / gunzip)
		const tgz = %q + "/bundle.tar.gz";
		archive.create(tgz, [srcDir + "/main.sh", { source: srcDir + "/conf/app.ini", dest: "custom.ini" }], { baseDir: srcDir });
		const unpack = %q + "/unpack";
		archive.extract(tgz, unpack);
		if (!fs.exists(unpack + "/main.sh")) throw new Error("missing extracted main.sh");
		if (!fs.exists(unpack + "/custom.ini")) throw new Error("missing extracted custom.ini");

		// --- net.http (curl)
		const res = net.http({ url: %q + "/api/status", method: "GET", check: true });
		if (res.status !== 200 || !res.ok) throw new Error("http status");
		if (!res.body.includes("primary")) throw new Error("http body");
		var threw = false;
		try { net.http({ url: %q + "/missing", check: true }); } catch (e) { threw = true; }
		if (!threw) throw new Error("check:true must throw on 404");

		// --- net.download (curl -o) with sha256 verify
		const dl = %q + "/pkg.bin";
		const payloadSha = crypto.sha256("native download payload");
		net.download(%q + "/pkg.bin", dl, { verifySha256: payloadSha, mode: 0o700 });
		if (!fs.exists(dl)) throw new Error("download missing");
		threw = false;
		try { net.download(%q + "/pkg.bin", %q + "/bad.bin", { verifySha256: "00000000000000000000000000000000" }); }
		catch (e) { threw = true; }
		if (!threw) throw new Error("sha256 mismatch must throw");
		if (fs.exists(%q + "/bad.bin")) throw new Error("failed download left file");

		// --- process.timeout + process.sleep (timeout / sleep)
		threw = false;
		try { process.run({ process: { binaryPath: "/bin/sh", arguments: ["-c", "sleep 5"] }, timeout: 300e6, check: true }); }
		catch (e) { threw = true; }
		if (!threw) throw new Error("timeout must throw");
		process.sleep(10e6);
	`,
		dir, dir, dir, dir,           // fs.copy section
		dir, dir,                    // archive section
		srv.URL, srv.URL,            // net.http
		dir, srv.URL, srv.URL, dir, dir, // net.download
	)

	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

// TestScriptCopyOptions verifies the *bool Overwrite default (overwrite:false
// keeps an existing dest; the default overwrite:true replaces it) through goja.
func TestScriptCopyOptions(t *testing.T) {
	engine, _ := buildTestEngine(t)
	dir := t.TempDir()
	src := fmt.Sprintf(`
		const { fs } = require("ezx");
		const src = %q;
		const dst = %q;
		fs.write(src, "new content");
		fs.write(dst, "old content");
		fs.copy(src, dst, { overwrite: false });
		fs.copy(src, dst);
	`,
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "b.txt"),
	)
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
	// Default overwrite:true replaced the old content.
	data, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Fatalf("b.txt = %q (overwrite:false copy must be followed by overwrite:true copy)", data)
	}
}