package lang

import (
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

func bootGrant(service string) grant.Grant {
	return grant.Grant{
		V: "sash/2", Actor: "service:" + service, Host: "web1", Purpose: grant.PurposeService, LeaseID: "boot-1",
		Entitlements: []string{"svc.spawn", "svc.signal", "svc.wait", "svc.proc", "svc.status", "svc.probe", "svc.output", "file.write", "file.read", "fs.mount"},
		FileRead:     []string{"/etc/app", "/run/app"},
		FileWrite:    []string{"/run/app", "/var/log/app"},
		Service:      service,
		Exec:         []string{"/opt/app/bin/server"},
		Caps:         []string{"CAP_NET_BIND_SERVICE"},
		Mounts:       []string{"/mnt/app"},
		MountSources: []string{"tmpfs", "/srv/app/static"},
		User:         "app",
		Group:        "app",
		EnvNames:     []string{"DB_PASS", "MODE", "P", "PORT", "LD_PRELOAD", "OPENSSL_CONF", "GIT_SSH_COMMAND"},
		Serial:       1,
	}
}

const appService = `service {
  name app
  description "App server"
  requires postgres
  after postgres network-online
  user app
  group app
  ready tcp 127.0.0.1:8080 timeout 60s
  stop signal TERM timeout 30s
  restart on-failure delay 5s burst 3 within 120s
}
`

func newInit(t *testing.T) *prototest.Init {
	t.Helper()
	in := prototest.NewInit("app", bootGrant("app"))
	in.Status["postgres"] = "ready"
	in.Procs["postgres"] = map[string]*prototest.Proc{"db": {Name: "db", State: "running"}}
	return in
}

func runEntry(t *testing.T, src, entry string, in *prototest.Init) Outcome {
	t.Helper()
	return Run(src, bootGrant("app"), in, Options{Entry: entry})
}

func TestServiceBlockParses(t *testing.T) {
	prog, err := parse(appService + "fn start() { proc main }\n")
	if err != nil {
		t.Fatal(err)
	}
	s := serviceOf(prog)
	if s == nil || s.Name != "app" || s.User != "app" || s.Ready.Kind != "tcp" || s.Ready.Target != "127.0.0.1:8080" ||
		s.Stop.Signal != "TERM" || s.Restart.Policy != "on-failure" || s.Restart.Burst != 3 || len(s.After) != 2 {
		t.Fatalf("service %+v", s)
	}
}

func TestServiceBlockRefusals(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"service {\n description x\n}\n", "service needs a name"},
		{"service {\n name App\n}\n", "service name: bad value"},
		{"service {\n name a\n name b\n}\n", "given twice"},
		{"service {\n name a\n colour red\n}\n", "unknown service key colour"},
		{"service {\n name a\n user $u\n}\n", "literal values"},
		{"service {\n name a\n run nginx\n}\n", "service run: bad value"},
		{"service {\n name a\n run \"/bin/sh -c x\"\n}\n", "service run: bad value"},
		{"service {\n name a\n ready tcp 8.8.8.8:53 timeout 1s\n}\n", "service ready"},
		{"service {\n name a\n ready tcp 127.0.0.1:80\n}\n", "service ready"},
		{"service {\n name a\n stop signal BOOM timeout 1s\n}\n", "service stop"},
		{"service {\n name a\n restart never delay 1s\n}\n", "service restart"},
		{"service {\n name a\n restart always burst 0 within 1s\n}\n", "service restart"},
		{"echo hi\nservice {\n name a\n}\n", "service must be the first"},
		{"fn f() { service {\n name a\n}\n}\n", "service must be the first"},
	} {
		if _, err := parse(c.src); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.src, err, c.want)
		}
	}
}

// A top-level statement that would run on --entry is refused at parse
// time, so --describe and --entry never run stray statements.
func TestServiceTopLevel(t *testing.T) {
	for _, top := range []string{
		"echo hi",
		"spawn x /opt/app/bin/server",
		"let x = (cat /etc/app/x)",
		"declare -s pw = (cat /etc/app/pw)",
		"if true { echo x }",
		"atomic { echo x > /run/app/x }",
		"let x = [ $y ]",
	} {
		_, err := parse(appService + top + "\n")
		if err == nil || !strings.Contains(err.Error(), "service_top_level") {
			t.Errorf("%q: %v", top, err)
		}
	}
	for _, ok := range []string{
		"args {\n port int default 8080\n}",
		"let port = 8080",
		"let ports = [ [80, 443] ]",
		"declare -r name = app",
		"use /etc/app/lib.sash",
		"fn helper(x) { echo $x }",
	} {
		if _, err := parse(appService + ok + "\n"); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	// Without a service block the same statements are an ordinary script.
	if _, err := parse("echo hi\n"); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRunAndStart(t *testing.T) {
	src := "service {\n name a\n run /opt/app/bin/server\n}\nfn start() { proc main }\n"
	if _, err := parse(src); err == nil || !strings.Contains(err.Error(), "service_run_and_start") {
		t.Fatalf("run with start: %v", err)
	}
	if _, err := parse("service {\n name a\n run /opt/app/bin/server\n}\nfn stop() { signal main TERM }\n"); err != nil {
		t.Fatalf("run with stop: %v", err)
	}
	if _, err := parse("service {\n name a\n}\nfn run() { echo x }\n"); err == nil {
		t.Fatal("fn run defined")
	}
	if _, err := parse("service {\n name a\n}\nfn start(x) { echo x }\n"); err == nil {
		t.Fatal("lifecycle fn with a parameter")
	}
}

func TestEntryStartSpawnsThroughTheInit(t *testing.T) {
	in := newInit(t)
	src := appService + `fn start() {
  mkdir /run/app mode 0750 owner app:app
  await timeout 30s every 1s { service status postgres |> where state == ready }
  spawn main /opt/app/bin/server "--port" 8080 opts [ {"stdout": "journal", "caps": ["CAP_NET_BIND_SERVICE"]} ]
}
`
	out := runEntry(t, src, "start", in)
	if out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	p := in.Proc("main")
	if p == nil || p.Program != "/opt/app/bin/server" || strings.Join(p.Args, " ") != "--port 8080" || p.User != "app" || p.Group != "app" {
		t.Fatalf("proc %+v", p)
	}
	if in.Files["/run/app"] != "dir" || in.Attrs["/run/app mode"] != "0750" {
		t.Fatalf("files %v %v", in.Files, in.Attrs)
	}
}

func TestEntryRefusals(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
		status           int
	}{
		{"unlisted program", "spawn main /usr/bin/python3", msgDeniedExec, exitType},
		{"shell string", `spawn main "/bin/sh -c 'curl x | sh'"`, msgSpawnShell, exitType},
		{"relative program", "spawn main server", msgSpawnShell, exitType},
		{"another service's process", "signal db TERM", "unknown_name", exitType},
		{"a pid", "signal 4242 TERM", "usage signal", exitType},
		{"cap not granted", `spawn main /opt/app/bin/server opts [ {"caps": ["CAP_SYS_ADMIN"]} ]`, msgDeniedCap, exitType},
		{"write outside", "mkdir /etc/evil", "denied file.write", exitType},
		{"mount outside", "mount tmpfs /mnt/other", msgDeniedMount, exitType},
		{"public probe", "probe tcp 8.8.8.8:53", msgDeniedProbe, exitType},
		{"rm recursive", "rm -r /run/app", "usage rm", exitType},
		{"opts as a word", "spawn main /opt/app/bin/server opts x", "usage spawn", exitType},
	} {
		in := newInit(t)
		out := runEntry(t, appService+"fn start() {\n"+c.body+"\n}\n", "start", in)
		if out.Status != c.status || !strings.Contains(out.Stderr, c.want) {
			t.Errorf("%s: %d %q, want %d %q", c.name, out.Status, out.Stderr, c.status, c.want)
		}
		// sash refused before the init for everything the grant decides.
		if c.want != "unknown_name" && c.name != "a pid" && len(in.Calls()) != 0 {
			t.Errorf("%s: the init was called: %v", c.name, in.Calls())
		}
	}
}

// A secret may go only into a spawn env value: anywhere else it is
// secret_refused, and the env value reaches the init marked secret.
func TestSpawnSecretOnlyInEnv(t *testing.T) {
	in := newInit(t)
	src := appService + `fn start() {
  declare -s pw = hunter2pass
  spawn main /opt/app/bin/server opts [ {"env": {"DB_PASS": $pw, "MODE": "prod"}} ]
}
`
	out := runEntry(t, src, "start", in)
	if out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	p := in.Proc("main")
	if p.Env["DB_PASS"] != "hunter2pass" || p.Env["MODE"] != "prod" || strings.Join(p.SecretEnv, ",") != "DB_PASS" {
		t.Fatalf("env %v secret %v", p.Env, p.SecretEnv)
	}
	for _, l := range in.Log() {
		if strings.Contains(l, "hunter2pass") {
			t.Fatalf("the secret is in the init's log: %q", l)
		}
	}
	for name, body := range map[string]string{
		"argument":   `spawn main /opt/app/bin/server $pw`,
		"user":       `spawn main /opt/app/bin/server opts [ {"user": $pw} ]`,
		"env map":    `let e = [ {"P": $pw} ]` + "\n" + `spawn main /opt/app/bin/server opts [ {"env": $e} ]`,
		"whole opts": `let o = [ {"env": {"P": $pw}} ]` + "\n" + `spawn main /opt/app/bin/server opts [ $o ]`,
		"signal":     `signal $pw TERM`,
		"mkdir":      `mkdir $pw`,
		"probe":      `probe tcp $pw`,
	} {
		in := newInit(t)
		out := runEntry(t, appService+"fn start() {\ndeclare -s pw = hunter2pass\n"+body+"\n}\n", "start", in)
		if out.Status != exitType || !strings.Contains(out.Stderr, "secret_refused") {
			t.Errorf("%s: %d %q", name, out.Status, out.Stderr)
		}
		if len(in.Calls()) != 0 {
			t.Errorf("%s: the init was called", name)
		}
	}
}

func TestSpawnPlanLine(t *testing.T) {
	in := newInit(t)
	st := NewState()
	st.LockPlan()
	src := appService + `fn start() {
  mkdir /run/app
  declare -s pw = hunter2pass
  spawn main /opt/app/bin/server "--name" "a b" opts [ {"env": {"P": $pw}} ]
  proc main
}
`
	out := Run(src, bootGrant("app"), in, Options{Entry: "start", State: st})
	if !strings.Contains(out.Stderr, "plan: would spawn main /opt/app/bin/server --name 'a b' as app:app\n") || !strings.Contains(out.Stderr, "plan: would mkdir /run/app\n") {
		t.Fatalf("%+v", out)
	}
	if strings.Contains(out.Stderr, "hunter2pass") {
		t.Fatal("the plan printed the secret")
	}
	// Reads still reach the init; mutations do not.
	calls := in.Calls()
	if len(calls) != 1 || calls[0].Key != "svc.proc" {
		t.Fatalf("calls %v", calls)
	}
}

func TestEntryCheckIsReadOnly(t *testing.T) {
	in := newInit(t)
	in.Ready["tcp 127.0.0.1:8080"] = true
	src := appService + "fn check() { probe tcp 127.0.0.1:8080 }\n"
	if out := runEntry(t, src, "check", in); out.Status != 0 {
		t.Fatalf("healthy: %+v", out)
	}
	in.Ready["tcp 127.0.0.1:8080"] = false
	if out := runEntry(t, src, "check", in); out.Status != 1 {
		t.Fatalf("unhealthy: %+v", out)
	}
	// A mutation in check is refused, plan mode or not.
	in = newInit(t)
	mut := appService + "fn check() { signal main TERM }\n"
	out := runEntry(t, mut, "check", in)
	if out.Status != exitType || !strings.Contains(out.Stderr, "await: mutation refused") || len(in.Calls()) != 0 {
		t.Fatalf("mutation in check: %+v %v", out, in.Calls())
	}
	st := NewState()
	st.LockPlan()
	out = Run(mut, bootGrant("app"), in, Options{Entry: "check", State: st})
	if out.Status != exitType || !strings.Contains(out.Stderr, "await: mutation refused") {
		t.Fatalf("mutation in check under plan: %+v", out)
	}
	// The check builtin still works in a service file beside fn check.
	both := appService + "fn check() { check \"up\" [ true ] }\n"
	if out := runEntry(t, both, "check", newInit(t)); out.Status != 0 || !strings.Contains(out.Stdout, "ok up") {
		t.Fatalf("check builtin: %+v", out)
	}
}

func TestEntryStatusesAndMissing(t *testing.T) {
	fakeClock(t)
	in := newInit(t)
	src := appService + "fn start() {\n await timeout 2s every 1s { probe tcp 127.0.0.1:8080 }\n}\nfn stop() { signal main TERM }\n"
	if out := runEntry(t, src, "start", in); out.Status != 3 {
		t.Fatalf("not ready: %+v", out)
	}
	if out := runEntry(t, src, "reload", in); out.Status != exitType || !strings.Contains(out.Stderr, "entry_missing: reload") {
		t.Fatalf("missing entry: %+v", out)
	}
	if out := runEntry(t, src, "bogus", in); out.Status != exitType || !strings.Contains(out.Stderr, "entry_missing") {
		t.Fatalf("bogus entry: %+v", out)
	}
	if out := runEntry(t, "echo hi\n", "start", in); out.Status != exitType || !strings.Contains(out.Stderr, "no service block") {
		t.Fatalf("no block: %+v", out)
	}
	if out := runEntry(t, appService+"fn start() { exit 7 }\n", "start", in); out.Status != 1 {
		t.Fatalf("exit 7: %+v", out)
	}
	if out := runEntry(t, appService+"fn start() { journal }\n", "start", in); out.Status != exitType {
		t.Fatalf("denied: %+v", out)
	}
}

// The grant's service must be the block's name before anything runs; a
// sash/1 grant is a mismatch too, and a boot grant needs an entry.
func TestGrantServiceMismatch(t *testing.T) {
	in := newInit(t)
	src := appService + "fn start() { spawn main /opt/app/bin/server }\n"
	for name, g := range map[string]grant.Grant{
		"other service": bootGrant("web"),
		"sash/1":        allow(),
	} {
		out := Run(src, g, in, Options{Entry: "start"})
		if out.Status != exitType || !strings.Contains(out.Stderr, "grant_service_mismatch") || len(in.Calls()) != 0 {
			t.Errorf("%s: %+v %v", name, out, in.Calls())
		}
	}
	if out := Run("echo hi\n", bootGrant("app"), in, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "entry_missing") || out.Stdout != "" {
		t.Fatalf("boot grant without an entry: %+v", out)
	}
}

func TestImplicitRunEntry(t *testing.T) {
	in := newInit(t)
	src := "service {\n name app\n user app\n group app\n run /opt/app/bin/server \"-g\" \"daemon off;\"\n}\n"
	if out := runEntry(t, src, "run", in); out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	p := in.Proc("main")
	if p == nil || strings.Join(p.Args, "|") != "-g|daemon off;" || p.User != "app" {
		t.Fatalf("proc %+v", p)
	}
	unlisted := "service {\n name app\n run /usr/bin/python3\n}\n"
	if out := runEntry(t, unlisted, "run", newInit(t)); out.Status != exitType || !strings.Contains(out.Stderr, msgDeniedExec) {
		t.Fatalf("unlisted run: %+v", out)
	}
	if out := runEntry(t, appService, "run", newInit(t)); out.Status != exitType || !strings.Contains(out.Stderr, "entry_missing: run") {
		t.Fatalf("no run: %+v", out)
	}
}

func TestStopWaitAndOutput(t *testing.T) {
	in := newInit(t)
	in.Procs["app"] = map[string]*prototest.Proc{"main": {Name: "main", State: "running", Pid: 7}}
	in.Output["main"] = []string{"a", "b", "c"}
	src := appService + `fn stop() {
  signal main TERM
  try { wait main timeout 25s } catch e { signal main KILL }
}
fn reload() {
  output main tail 2 |> select text
}
`
	if out := runEntry(t, src, "stop", in); out.Status != 0 || in.Proc("main").Signal != "TERM" {
		t.Fatalf("stop: %+v", out)
	}
	out := runEntry(t, src, "reload", in)
	if out.Status != 0 || !strings.Contains(out.Stdout, "b") || strings.Contains(out.Stdout, "a\n") {
		t.Fatalf("output: %+v", out)
	}
	// wait on a process still running is exit 2 timeout, which try catches.
	in = newInit(t)
	in.Procs["app"] = map[string]*prototest.Proc{"main": {Name: "main", State: "running"}}
	if out := runEntry(t, appService+"fn stop() { wait main timeout 1s }\n", "stop", in); out.Status != exitType || !strings.Contains(out.Stderr, "timeout") {
		t.Fatalf("wait timeout: %+v", out)
	}
}

func TestServiceNeedsAndLint(t *testing.T) {
	src := appService + `fn start() {
  mkdir /run/app
  spawn main /opt/app/bin/server opts [ {"caps": ["CAP_NET_BIND_SERVICE"], "stdout": "file:/var/log/app/out.log"} ]
  probe tcp 127.0.0.1:8080
  mount tmpfs /run/app/tmp
}
`
	out := Needs(src, NeedsOptions{})
	for _, want := range []string{"svc.spawn", "/opt/app/bin/server", "cap CAP_NET_BIND_SERVICE", "/var/log/app/out.log", "svc.probe", "tcp 127.0.0.1:8080", "fs.mount", "/run/app/tmp"} {
		if !strings.Contains(out.Stdout, want) {
			t.Errorf("needs lacks %q:\n%s", want, out.Stdout)
		}
	}
	if out.Status != 0 {
		t.Errorf("literal needs incomplete: %+v", out)
	}
	pol := Needs(src, NeedsOptions{Policy: true})
	if !strings.Contains(pol.Stdout, "# svc.spawn /opt/app/bin/server: only a sash/2 boot grant") {
		t.Errorf("policy:\n%s", pol.Stdout)
	}
	dyn := Needs(appService+"fn start() {\nlet o = [ {} ]\nspawn main /opt/app/bin/server opts [ $o ]\n}\n", NeedsOptions{})
	if dyn.Status != 1 || !strings.Contains(dyn.Stdout, "dynamic") {
		t.Errorf("dynamic opts: %+v", dyn)
	}
}

func TestServiceLintRules(t *testing.T) {
	out := Lint(appService + "fn start() {\n spawn main /bin/sh \"-c\" \"x\"\n}\nfn check() {\n mkdir /run/app\n}\n")
	if !strings.Contains(out.Stdout, "spawn-shell") || !strings.Contains(out.Stdout, "check-mutates  error") {
		t.Fatalf("lint:\n%s", out.Stdout)
	}
	if out := Lint("service {\n name a\n run /usr/bin/env python3\n}\n"); !strings.Contains(out.Stdout, "spawn-shell") {
		t.Fatalf("run of env:\n%s", out.Stdout)
	}
	clean := Lint(appService + "fn start() {\n spawn main /opt/app/bin/server\n}\nfn check() {\n probe tcp 127.0.0.1:8080\n}\n")
	if strings.Contains(clean.Stdout, "spawn-shell") || strings.Contains(clean.Stdout, "check-mutates") {
		t.Fatalf("clean:\n%s", clean.Stdout)
	}
}

// A recording of a spawn whose env holds a secret keeps neither the
// secret nor a hash of the options.
func TestSpawnSecretNotRecorded(t *testing.T) {
	var buf strings.Builder
	// The secret has characters JSON escapes, so a recording of the body
	// as sent would hold it in another spelling.
	src := appService + "fn start() {\n declare -s pw = 'hunter2<b&c>d\"\\\\'\n spawn main /opt/app/bin/server opts [ {\"env\": {\"P\": $pw}} ]\n}\n"
	tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if out := Run(src, bootGrant("app"), newInit(t), Options{Entry: "start", Tape: tape}); out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	if err := tape.Finish(); err != nil {
		t.Fatal(err)
	}
	if r := buf.String(); strings.Contains(r, "hunter2") || strings.Contains(r, "body_sha256") || !strings.Contains(r, `"redacted":true`) {
		t.Fatalf("recording: %s", r)
	}
}

// No service verb takes a flag: under a prefix, rm -rf would otherwise be
// the path <prefix>/-rf.
func TestServiceVerbsTakeNoFlags(t *testing.T) {
	g := bootGrant("app")
	g.V, g.Purpose = "sash/1", grant.PurposeOpen
	for _, line := range []string{"rm -rf", "chmod -R 0755", "touch -d", "umount -l"} {
		f := &fake{}
		out := Run("cd /run/app\n"+line+"\n", g, f, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "usage") {
			t.Errorf("%q: %+v", line, out)
		}
		for _, c := range f.calls {
			if c.Key != "file.read" {
				t.Errorf("%q reached the parent: %+v", line, c)
			}
		}
	}
}

func TestSpawnIdentityIsTheGrants(t *testing.T) {
	other := strings.Replace(appService, "user app", "user root", 1)
	in := newInit(t)
	if out := runEntry(t, other+"fn start() { spawn main /opt/app/bin/server }\n", "start", in); out.Status != exitType || !strings.Contains(out.Stderr, "grant_identity_mismatch") || len(in.Calls()) != 0 {
		t.Fatalf("block user root: %+v", out)
	}
	for opt, want := range map[string]string{`"user": "root"`: "denied user", `"group": "wheel"`: "denied group", `"user": "www"`: "denied user"} {
		in := newInit(t)
		out := runEntry(t, appService+"fn start() { spawn main /opt/app/bin/server opts [ {"+opt+"} ] }\n", "start", in)
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || len(in.Calls()) != 0 {
			t.Errorf("%s: %+v", opt, out)
		}
	}
	// root only when the grant says root.
	g := bootGrant("app")
	g.User = "root"
	in = prototest.NewInit("app", g)
	if out := Run(other+"fn start() { spawn main /opt/app/bin/server opts [ {\"user\": \"root\"} ] }\n", g, in, Options{Entry: "start"}); out.Status != 0 || in.Proc("main").User != "root" {
		t.Fatalf("root grant: %+v", out)
	}
	// The init re-checks: a spawn sash did not shape is refused there too.
	in = newInit(t)
	if _, err := in.Do(parent.Verb{Key: "svc.spawn", Argv: []string{"spawn", "main", "/opt/app/bin/server"}, Body: []byte(`{"user":"root","group":"app"}`)}); err == nil {
		t.Fatal("the init took user root")
	}
	if _, err := in.Do(parent.Verb{Key: "svc.spawn", Argv: []string{"spawn", "main", "/opt/app/bin/server"}, Body: []byte(`{"user":"app","group":"app","env":{"LD_PRELOAD":"/tmp/x.so"}}`)}); err == nil || err.Error() != "env_refused" {
		t.Fatalf("the init took LD_PRELOAD: %v", err)
	}
}

func TestSpawnEnvRefused(t *testing.T) {
	for _, name := range []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "MALLOC_CHECK_", "GCONV_PATH", "GLIBC_TUNABLES", "HOSTALIASES", "PYTHONPATH", "NODE_OPTIONS", "BASH_ENV"} {
		in := newInit(t)
		out := runEntry(t, appService+"fn start() { spawn main /opt/app/bin/server opts [ {\"env\": {\""+name+"\": \"x\"}} ] }\n", "start", in)
		if out.Status != exitType || !strings.Contains(out.Stderr, "env_refused: "+name) || len(in.Calls()) != 0 {
			t.Errorf("%s: %+v", name, out)
		}
	}
	if out := runEntry(t, appService+"fn start() { spawn main /opt/app/bin/server opts [ {\"env\": {\"PORT\": \"80\"}} ] }\n", "start", newInit(t)); out.Status != 0 {
		t.Fatalf("PORT: %+v", out)
	}
}

func TestMountSourceAndOverlap(t *testing.T) {
	for body, want := range map[string]string{
		"mount / /mnt/app opts bind":                  "denied mount source",
		"mount /etc /mnt/app opts rbind":              "denied mount source",
		"mount /srv/app/static/../../../etc /mnt/app": "usage mount",
		"mount proc /mnt/app type proc":               "denied mount source",
		"mount tmpfs /mnt/app opts rbind":             "usage mount: a bind takes a path",
		"mount /srv/app/static /mnt/app type tmpfs":   "mount_opt_refused",
		"mount /srv/app/static /mnt/app type bind":    "mount_opt_refused",
		"mount tmpfs /mnt/app":                        "mount_opt_refused",
		"mount tmpfs /mnt/app type overlay opts \"lowerdir=/etc,upperdir=/srv/u,workdir=/srv/w\"": "mount_opt_refused",
		"mount tmpfs /mnt/app type tmpfs opts \"suid,dev,exec\"":                                  "mount_opt_refused",
		"mount tmpfs /mnt/app type tmpfs opts x-bind":                                             "mount_opt_refused",
		"mount tmpfs /mnt/app type tmpfs opts move":                                               "mount_opt_refused",
		"mount /srv/app/static /mnt/app opts BIND":                                                "mount_opt_refused",
		"mount tmpfs /mnt/app type TMPFS":                                                         "mount_opt_refused",
		"mount tmpfs /mnt/app type tmpfs opts \"mode=7777x\"":                                     "mount_opt_refused",
		"mount /srv/app/static/img /mnt/app opts \"ro,bind\"":                                     "",
		"mount tmpfs /mnt/app type tmpfs opts \"size=64m,mode=0755,uid=100,noexec\"":              "",
	} {
		in := newInit(t)
		out := runEntry(t, appService+"fn start() {\n"+body+"\n}\n", "start", in)
		if want == "" {
			// sash always adds nosuid and nodev, and the init requires them.
			calls := in.Calls()
			if out.Status != 0 || len(in.Mounts) != 1 || len(calls) != 1 || !strings.HasSuffix(strings.Join(calls[0].Argv, " "), ",nosuid,nodev") {
				t.Errorf("%q: %+v %v", body, out, calls)
			}
			continue
		}
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || len(in.Calls()) != 0 {
			t.Errorf("%q: %+v", body, out)
		}
	}
	// The reviewer's repro: a mount point inside file_write would turn
	// chmod and chown onto the mounted tree; one holding a write root too.
	for _, mounts := range []string{"/run/app/tmp", "/run"} {
		g := bootGrant("app")
		g.Mounts, g.MountSources = []string{mounts}, []string{"/etc"}
		in := prototest.NewInit("app", g)
		out := Run(appService+"fn start() {\nmount /etc "+mounts+" opts bind\nchmod /run/app/tmp/shadow 0666\n}\n", g, in, Options{Entry: "start"})
		if out.Status != exitType || !strings.Contains(out.Stderr, "mount_overlap") || len(in.Calls()) != 0 {
			t.Errorf("%s: %+v %v", mounts, out, in.Calls())
		}
		if _, err := in.Do(parent.Verb{Key: "fs.mount", Argv: []string{"mount", "/etc", mounts, "opts", "bind,nosuid,nodev"}}); err == nil || err.Error() != "mount_overlap" {
			t.Errorf("%s: the init mounted: %v", mounts, err)
		}
	}
	in := newInit(t)
	for _, argv := range [][]string{
		{"mount", "/", "/mnt/app", "opts", "bind,nosuid,nodev"},
		{"mount", "tmpfs", "/mnt/app", "type", "tmpfs", "opts", "size=1m"},
		{"mount", "tmpfs", "/mnt/app", "type", "overlay", "opts", "nosuid,nodev,lowerdir=/etc"},
		{"mount", "tmpfs", "/mnt/app", "type", "tmpfs", "opts", "nosuid,nodev,suid"},
	} {
		if _, err := in.Do(parent.Verb{Key: "fs.mount", Argv: argv}); err == nil {
			t.Errorf("the init mounted %v", argv)
		}
	}
}

// A glob or brace pattern in a service file would read the host at load,
// before the top level: refused before expansion, with no parent call.
func TestServiceGlobRefused(t *testing.T) {
	for _, src := range []string{
		appService + "echo /etc/app/*\n",
		appService + "fn start() {\n cat /etc/app/*.conf\n}\n",
		appService + "fn start() {\n cat /etc/app/{a,b}\n}\n",
	} {
		in := newInit(t)
		out := runEntry(t, src, "start", in)
		if out.Status != exitType || !strings.Contains(out.Stderr, "service_glob") || len(in.Calls()) != 0 {
			t.Errorf("%q: %+v", src, out)
		}
		f := &fake{}
		if out := Run(src, allow(), f, Options{}); !strings.Contains(out.Stderr, "service_glob") || len(f.calls) != 0 {
			t.Errorf("no entry %q: %+v", src, out)
		}
		if d := Describe(src, DescribeOptions{}); d.Status != exitType || !strings.Contains(d.Stderr, "service_glob") {
			t.Errorf("describe %q: %+v", src, d)
		}
	}
	// A quoted * and a [ ] product are not globs.
	if out := runEntry(t, appService+"fn start() {\n spawn main /opt/app/bin/server \"*\" [ 2 * 3 ]\n}\n", "start", newInit(t)); out.Status != 0 {
		t.Fatalf("quoted star: %+v", out)
	}
	// An ordinary script still globs.
	if out := Run("echo /etc/*", allow(), &listParent{}, Options{}); strings.Contains(out.Stderr, "service_glob") {
		t.Fatalf("plain script: %+v", out)
	}
}

// The init does not follow a symlink at any component of a file verb's
// path.
func TestInitRefusesSymlinks(t *testing.T) {
	for _, line := range []string{"chown /run/app/x app:app", "chmod /run/app/x 0644", "rm /run/app/x", "touch /run/app/l/y", "mkdir /run/app/l/d"} {
		in := newInit(t)
		in.Files["/run/app/x"] = "link /etc/shadow"
		in.Files["/run/app/l"] = "link /etc"
		out := runEntry(t, appService+"fn start() {\n"+line+"\n}\n", "start", in)
		if out.Status == 0 || in.Files["/run/app/x"] != "link /etc/shadow" || in.Attrs["/run/app/x owner"] != "" {
			t.Errorf("%q: %+v %v", line, out, in.Attrs)
		}
	}
}

// A sash/1 grant listing a boot key still runs no service verb.
func TestBootKeysNeedABootGrant(t *testing.T) {
	g := bootGrant("app")
	g.V, g.Purpose = "sash/1", grant.PurposeOpen
	for _, line := range []string{"spawn main /opt/app/bin/server", "signal main TERM", "proc main", "service status postgres", "probe tcp 127.0.0.1:80", "mount tmpfs /mnt/app"} {
		f := &fake{}
		out := Run(line, g, f, Options{})
		if out.Status != exitDenied || len(f.calls) != 0 {
			t.Errorf("%q: %+v %v", line, out, f.calls)
		}
	}
}

// The env names are an allowlist signed in the grant; the denylist is a
// second gate the grant cannot open.
func TestSpawnEnvAllowlist(t *testing.T) {
	spawn := func(name string, g grant.Grant) (Outcome, *prototest.Init) {
		in := prototest.NewInit("app", g)
		return Run(appService+"fn start() { spawn main /opt/app/bin/server opts [ {\"env\": {\""+name+"\": \"x\"}} ] }\n", g, in, Options{Entry: "start"}), in
	}
	g := bootGrant("app")
	for _, name := range []string{"OPENSSL_CONF", "CLASSPATH", "PYTHONWARNINGS", "PERL5DB", "GIT_SSH_COMMAND", "PATH", "LD_PRELOAD", "lower", "OPENSSL_ENGINES"} {
		out, in := spawn(name, g)
		if out.Status != exitType || !strings.Contains(out.Stderr, "env_refused: "+name) || len(in.Calls()) != 0 {
			t.Errorf("%s: %+v", name, out)
		}
	}
	// PATH is allowed only when the grant lists it.
	g.EnvNames = append(g.EnvNames, "PATH")
	if out, in := spawn("PATH", g); out.Status != 0 || in.Proc("main").Env["PATH"] != "x" {
		t.Fatalf("listed PATH: %+v", out)
	}
	// The init applies both gates itself.
	in := prototest.NewInit("app", bootGrant("app"))
	for _, name := range []string{"CLASSPATH", "LD_PRELOAD", "GIT_SSH_COMMAND"} {
		body := `{"user":"app","group":"app","env":{"` + name + `":"x"}}`
		if _, err := in.Do(parent.Verb{Key: "svc.spawn", Argv: []string{"spawn", "main", "/opt/app/bin/server"}, Body: []byte(body)}); err == nil || err.Error() != "env_refused" {
			t.Errorf("init %s: %v", name, err)
		}
	}
}

func TestSpawnCwd(t *testing.T) {
	for cwd, want := range map[string]string{"/root": "denied cwd", "/etc": "denied cwd", "/run/app/data": "", "/etc/app": ""} {
		in := newInit(t)
		out := runEntry(t, appService+"fn start() { spawn main /opt/app/bin/server opts [ {\"cwd\": \""+cwd+"\"} ] }\n", "start", in)
		if want == "" && out.Status != 0 || want != "" && (out.Status != exitType || !strings.Contains(out.Stderr, want) || len(in.Calls()) != 0) {
			t.Errorf("%s: %+v", cwd, out)
		}
	}
	in := newInit(t)
	if _, err := in.Do(parent.Verb{Key: "svc.spawn", Argv: []string{"spawn", "main", "/opt/app/bin/server"}, Body: []byte(`{"user":"app","group":"app","cwd":"/root"}`)}); err == nil {
		t.Fatal("the init took cwd /root")
	}
}

func TestGrantRequestEnvNames(t *testing.T) {
	src := appService + "fn start() {\n spawn main /opt/app/bin/server opts [ {\"env\": {\"PORT\": \"80\", \"LD_PRELOAD\": \"/x\"}} ]\n}\n"
	out := Describe(src, DescribeOptions{GrantRequest: true})
	if out.Status != 0 || !strings.Contains(out.Stdout, "\"env_names\": [\n    \"PORT\"\n  ]") {
		t.Fatalf("%+v", out)
	}
	dyn := Describe(appService+"fn start() {\n let e = [ {} ]\n spawn main /opt/app/bin/server opts [ {\"env\": $e} ]\n}\n", DescribeOptions{})
	if dyn.Status != 1 || !strings.Contains(dyn.Stdout, "\"target\": \"env $\"") {
		t.Fatalf("dynamic env: %+v", dyn)
	}
}

// A type mount's source is its type: a grant listing only tmpfs cannot
// mount proc or sysfs by naming tmpfs as the source.
func TestMountTypeIsTheSource(t *testing.T) {
	for _, body := range []string{"mount tmpfs /mnt/app type proc", "mount tmpfs /mnt/app type sysfs", "mount proc /mnt/app type proc"} {
		in := newInit(t)
		out := runEntry(t, appService+"fn start() {\n"+body+"\n}\n", "start", in)
		if out.Status == 0 || len(in.Calls()) != 0 {
			t.Errorf("%q: %+v", body, out)
		}
	}
	g := bootGrant("app")
	g.MountSources = append(g.MountSources, "proc")
	in := prototest.NewInit("app", g)
	if out := Run(appService+"fn start() {\nmount proc /mnt/app type proc\n}\n", g, in, Options{Entry: "start"}); out.Status != 0 {
		t.Fatalf("listed proc: %+v", out)
	}
	in = newInit(t)
	if _, err := in.Do(parent.Verb{Key: "fs.mount", Argv: []string{"mount", "tmpfs", "/mnt/app", "type", "proc", "opts", "nosuid,nodev"}}); err == nil {
		t.Fatal("the init mounted proc from a tmpfs source")
	}
	req := Describe(appService+"fn start() {\nmount proc /mnt/app type proc\n}\n", DescribeOptions{GrantRequest: true})
	if !strings.Contains(req.Stdout, "\"mount_sources\": [\n    \"proc\"\n  ]") {
		t.Fatalf("request:\n%s", req.Stdout)
	}
}

// Option values are bounded by meaning, and ro with rw is refused.
func TestMountOptionBounds(t *testing.T) {
	for opts, ok := range map[string]bool{
		"uid=4294967294": true, "uid=4294967295": false, "gid=9999999999": false,
		"mode=1777": true, "mode=0755": true, "mode=2755": false, "mode=4755": false, "mode=7777": false,
		"ro,rw": false, "ro": true, "size=64m": true, "size=100%": true, "size=101%": false,
		"size=999999999999999g": false, "size=999999999999999": true, "nr_inodes=10k": true, "nr_inodes=5%": false,
	} {
		_, err := grant.CheckMountOpts("tmpfs", "tmpfs", strings.Split(opts, ","))
		if (err == nil) != ok {
			t.Errorf("%s: %v", opts, err)
		}
	}
}

// remount with a type is valid (it changes the options of a mount this
// service made); the init refuses a remount of a directory it did not
// mount for the service.
func TestMountRemount(t *testing.T) {
	in := newInit(t)
	src := appService + "fn start() {\nmount tmpfs /mnt/app type tmpfs opts \"remount,ro\"\n}\n"
	if out := runEntry(t, src, "start", in); out.Status == 0 || !strings.Contains(out.Stderr, "not_mounted") {
		t.Fatalf("remount of nothing: %+v", out)
	}
	in.Mounts["/mnt/app"] = "tmpfs"
	if out := runEntry(t, src, "start", in); out.Status != 0 {
		t.Fatalf("remount: %+v", out)
	}
}
