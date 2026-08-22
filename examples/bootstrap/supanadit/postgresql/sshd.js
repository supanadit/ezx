// sshd.js — init/05-sshd: generate host keys and replica SSH keys (mirrors the
// bash init/05-sshd.sh).
const { fs, editor } = require("ezx");
const { runPg } = require("./database");

function sshdKeys() {
	const run = "/run/ssh";
	fs.mkdirAll(run, 0o755);
	if (!fs.exists(run + "/ssh_host_rsa_key"))
		runPg("ssh-keygen -A", "ssh-keygen");
	const dir = "/home/postgres/.ssh";
	fs.mkdirAll(dir, 0o700);
	fs.chmod(dir, 0o700);
	if (!fs.exists(dir + "/id_rsa")) {
		runPg(
			"ssh-keygen -t rsa -b 4096 -f " + dir + "/id_rsa -N '' -C 'replica'",
			"ssh-key",
		);
		fs.chmod(dir + "/id_rsa", 0o600);
	}
	if (fs.exists(dir + "/id_rsa.pub") && !fs.exists(dir + "/authorized_keys")) {
		editor.open(dir + "/authorized_keys").replace(
			editor
				.open(dir + "/id_rsa.pub")
				.read()
				.trim() + "\n",
		);
	}
}

module.exports = { sshdKeys };