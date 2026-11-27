package main

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
)

const profileMarker = "PROFILE_WAS_READ"

// loginHome is a home directory whose every startup file would print a
// marker if any shell, sash included, read it.
func loginHome(t *testing.T) (home string, env map[string]string) {
	t.Helper()
	home = t.TempDir()
	body := "echo " + profileMarker + "\n"
	for _, name := range []string{".profile", ".sashrc", ".sash_profile", ".sash_login", ".bashrc", ".bash_profile", ".bash_login", ".zshrc", ".zprofile", ".login", ".env", "envfile"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env = map[string]string{
		"HOME": home, "ENV": filepath.Join(home, "envfile"), "BASH_ENV": filepath.Join(home, "envfile"),
		"SHELL": "/bin/bash", "PATH": "/usr/bin:/bin", "USER": "nobody", "FOO": "from-the-environment", "TERM": "xterm",
	}
	return home, env
}

// As a login shell sash starts the prompt with an empty grant, says so
// once, and reads no startup file: argv[0] -sash, -l, and --login alike.
func TestBinaryLoginShellStartsThePrompt(t *testing.T) {
	bin := ptyBin(t)
	home, env := loginHome(t)
	for _, c := range []struct {
		name  string
		argv0 string
		args  []string
	}{{"argv[0] -sash", "-sash", nil}, {"-l", "", []string{"-l"}}, {"--login", "", []string{"--login"}}, {"-sash -l", "-sash", []string{"-l"}}, {"plain", "", nil}} {
		rep := runPTY(t, ptyRun{
			Argv: append([]string{bin}, c.args...), Argv0: c.argv0, Env: env, Cwd: home,
			Steps: []ptyStep{
				{Expect: "sash> "}, {Send: "echo filters run\r"}, {Expect: "\r\nfilters run\r\n"},
				// The prompt refuses a denied or unknown line before it
				// runs; Ctrl-C clears the line.
				{Send: "unit restart nginx\r"}, {Expect: "denied unit.restart"}, {Send: "\x03"},
				{Send: "scp -t /tmp\r"}, {Expect: "unknown"}, {Send: "\x03"},
				{Send: "echo $FOO\r"}, {Expect: "unset FOO"}, {Send: "\x03"},
				{Send: "echo done\r"}, {Expect: "\r\ndone\r\n"},
				{Send: "\x04"},
			},
		})
		if !rep.Restored || strings.Count(rep.TTY, msgHandStart) != 1 || !strings.HasPrefix(rep.TTY, msgHandStart+"\r\n") {
			t.Errorf("%s: code %d restored %v tty %q", c.name, rep.Code, rep.Restored, rep.TTY)
		}
		if strings.Contains(rep.TTY, profileMarker) || strings.Contains(rep.TTY, "from-the-environment") {
			t.Errorf("%s: a startup file or the environment was read: %q", c.name, rep.TTY)
		}
	}
}

// sshd runs $SHELL -c "<command>": that is the -c path, with or without
// -l, and it prints no notice. Nothing is executed: scp, sftp, rsync, and
// git are exit 127. A parse error is exit 2 and falls through to nothing.
func TestBinaryLoginShellRunsNoOtherProgram(t *testing.T) {
	bin := ptyBin(t)
	home, env := loginHome(t)
	marker := filepath.Join(home, "ran")
	for _, c := range []struct {
		argv0 string
		args  []string
		code  int
		out   string
		errs  string
	}{
		{"", []string{"-l", "-c", "echo a"}, 0, "a\n", ""},
		{"-sash", []string{"-c", "echo a"}, 0, "a\n", ""},
		{"-sash", []string{"--login", "-c", "echo a"}, 0, "a\n", ""},
		{"-sash", []string{"-c", "scp -t /tmp"}, 127, "", "unknown scp"},
		{"-sash", []string{"-c", "sftp-server"}, 127, "", "unknown sftp-server"},
		{"-sash", []string{"-c", "/usr/lib/openssh/sftp-server"}, 127, "", "unknown"},
		{"-sash", []string{"-c", "rsync --server -vlogDtprze.iLsfxC . /tmp"}, 127, "", "unknown rsync"},
		{"-sash", []string{"-c", "git-upload-pack '/srv/repo.git'"}, 127, "", "unknown git-upload-pack"},
		{"-sash", []string{"-c", "touch " + marker}, 127, "", "unknown touch"},
		{"-sash", []string{"-c", "sh -c 'touch " + marker + "'"}, 127, "", "unknown sh"},
		{"-sash", []string{"-c", "touch " + marker + " &"}, 2, "", "lex:"},
		{"-sash", []string{"-c", "echo $FOO $HOME"}, 2, "", "unset FOO"},
		{"-sash", []string{"-c", "echo $SHELL"}, 2, "", "unset SHELL"},
		{"-sash", []string{"-l", "-l", "-c", "echo a"}, 2, "", "usage"},
		{"-sash", []string{"-c", "echo a", "-l"}, 2, "", "usage"},
		{"-sash", []string{"--plan", "-l", "-c", "echo a"}, 2, "", "usage"},
	} {
		rep := runPTY(t, ptyRun{Argv: append([]string{bin}, c.args...), Argv0: c.argv0, Env: env, Cwd: home, Stdin: "pipe", Stdout: "pipe", Stderr: "pipe"})
		if rep.Code != c.code || rep.Stdout != c.out || !strings.Contains(rep.Stderr, c.errs) {
			t.Errorf("%q: code %d stdout %q stderr %q", c.args, rep.Code, rep.Stdout, rep.Stderr)
		}
		if strings.Contains(rep.Stdout+rep.Stderr, profileMarker) || strings.Contains(rep.Stdout+rep.Stderr, msgHandStart) || strings.Contains(rep.Stdout, "from-the-environment") {
			t.Errorf("%q: a startup file was read, or a script got the notice: stdout %q stderr %q", c.args, rep.Stdout, rep.Stderr)
		}
	}
	// What other shells would run as a substitution is, here, never run:
	// whatever sash makes of the text, no program starts.
	for _, src := range []string{"echo $(touch " + marker + ")", "echo `touch " + marker + "`", "eval touch " + marker, "exec touch " + marker, "source " + filepath.Join(home, ".profile"), ". " + filepath.Join(home, ".profile")} {
		rep := runPTY(t, ptyRun{Argv: []string{bin, "-l", "-c", src}, Argv0: "-sash", Env: env, Cwd: home, Stdin: "pipe", Stdout: "pipe", Stderr: "pipe"})
		if strings.Contains(rep.Stdout+rep.Stderr, profileMarker) {
			t.Errorf("%q: the profile was read: stdout %q stderr %q", src, rep.Stdout, rep.Stderr)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a host program ran and wrote %s", marker)
	}
	// A script on stdin, as ssh host < script gives a login shell.
	rep := runPTY(t, ptyRun{Argv: []string{bin, "-l"}, Argv0: "-sash", Env: env, Cwd: home, Stdin: "pipe", Input: "echo piped\n", Stdout: "pipe", Stderr: "pipe"})
	if rep.Code != 0 || rep.Stdout != "piped\n" || rep.Stderr != "" {
		t.Errorf("stdin script: code %d stdout %q stderr %q", rep.Code, rep.Stdout, rep.Stderr)
	}
}

// A real session is not a hand start: under --session the prompt prints
// no notice.
func TestBinarySessionPromptHasNoHandStartNotice(t *testing.T) {
	bin := ptyBin(t)
	fleetPub, fleetPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	emergencyPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := grant.Sign(grant.Grant{
		V: "sash/1", Actor: "ryan", Host: "miniapps1", Purpose: grant.PurposeOpen,
		Exp: time.Now().Add(time.Hour).Unix(), LeaseID: "lease-1", Entitlements: []string{"ps"},
	}, fleetPriv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string][]byte{"grant": doc, "fleet": fleetPub, "emergency": emergencyPub}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The shell only hands the session its descriptors, as the agent does.
	rep := runPTY(t, ptyRun{
		Argv: []string{"/bin/sh", "-c", `exec "$0" --session 3<grant 4<fleet 5<emergency 6</dev/null`, bin}, Cwd: dir,
		Steps: []ptyStep{{Expect: "sash> "}, {Send: "echo in a session\r"}, {Expect: "\r\nin a session\r\n"}, {Send: "\x04"}},
	})
	if rep.Code != 0 || strings.Contains(rep.TTY, "no session grant") {
		t.Errorf("code %d tty %q", rep.Code, rep.TTY)
	}
}

func TestLoginArgs(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "-l": "", "--login": "", "-l -c x": "-c x", "--login --plan f": "--plan f", "-l -l": "-l",
		"-c -l": "-c -l", "--plan -l": "--plan -l", "-login": "-login", "-L": "-L", "--session -l": "--session -l",
	} {
		if got := strings.Join(loginArgs(strings.Fields(in)), " "); got != want {
			t.Errorf("loginArgs(%q) = %q, want %q", in, got, want)
		}
	}
}
