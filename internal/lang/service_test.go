package lang

import (
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
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
		Mounts:       []string{"/run/app/tmp"},
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
		{"a pid", "signal 4242 TERM", "unknown_name", exitType},
		{"cap not granted", `spawn main /opt/app/bin/server opts [ {"caps": ["CAP_SYS_ADMIN"]} ]`, msgDeniedCap, exitType},
		{"write outside", "mkdir /etc/evil", "denied file.write", exitType},
		{"mount outside", "mount tmpfs /mnt", msgDeniedMount, exitType},
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
	src := appService + "fn start() {\n declare -s pw = hunter2pass\n spawn main /opt/app/bin/server opts [ {\"env\": {\"P\": $pw}} ]\n}\n"
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
	if r := buf.String(); strings.Contains(r, "hunter2pass") || strings.Contains(r, "body_sha256") || !strings.Contains(r, `"redacted":true`) {
		t.Fatalf("recording: %s", r)
	}
}
