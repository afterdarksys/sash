package lang

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

func TestPipelineFunctions(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`fn greeting() { echo hello }; greeting | egrep hello`, "hello\n"},
		{`fn loud() { return [ upper($input) ] }; echo hello | loud | egrep HELLO`, "HELLO\n"},
		{`fn identity() { return $input }; ps |> identity |> where pid == 1 |> select pid`, "1\n"},
		{`fn source() { return [ [{"state":"active"}] ] }; source |> where state == active |> select state`, "active\n"},
		{`let input = outer; fn identity() { return $input }; echo inner | identity | egrep inner; echo $input`, "inner\nouter\n"},
		{`fn outer() { fn inner() { return $input }; let x = (echo nested | inner); return $input }; echo original | outer | egrep original`, "original\n"},
		{`fn read() { return $input }; read <<< hi | egrep hi`, "hi\n"},
	} {
		t.Run(tc.src, func(t *testing.T) {
			out := Run(tc.src, allow(), &fake{}, Options{})
			if out.Status != 0 || out.Stdout != tc.want {
				t.Fatalf("%+v want %q", out, tc.want)
			}
		})
	}
}

func TestPipelineFunctionRefusals(t *testing.T) {
	for _, src := range []string{
		`fn text() { return hello }; text |> where a == b`,
		`fn mixed() { echo debug; return hello }; mixed | cat`,
		`fn secret() { declare -s tok = hidden; return $tok }; secret | cat`,
		`fn secret() { declare -s tok = hidden; return $tok }; secret > /tmp/out`,
	} {
		p := &fake{}
		out := Run(src, grant.Grant{Entitlements: []string{"file.write"}, FileWrite: []string{"/tmp"}}, p, Options{})
		if out.Status != exitType || len(p.calls) != 0 || strings.Contains(out.Stdout, "hidden") {
			t.Fatalf("%s: %+v calls %v", src, out, p.calls)
		}
	}
	p := &fake{}
	out := Run(`fn read() { ps }; read | cat > /outside/out`, allow(), p, Options{})
	if out.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("preflight: %+v calls %v", out, p.calls)
	}
	out = Run(`fn leave() { exit 7 }; leave | echo no; echo after`, allow(), &fake{}, Options{})
	if !out.Exit || out.Status != 7 || out.Stdout != "" {
		t.Fatalf("exit: %+v", out)
	}
}

type readinessParent struct {
	n     int
	ready int
	clock *time.Time
	step  time.Duration
	calls []parent.Verb
}

func (p *readinessParent) Do(v parent.Verb) (parent.Result, error) {
	p.n++
	p.calls = append(p.calls, v)
	if p.clock != nil {
		*p.clock = p.clock.Add(p.step)
	}
	state := "starting"
	if p.n >= p.ready {
		state = "active"
	}
	return parent.Result{Kind: parent.KindRows, Cols: []string{"state"}, Rows: []parent.Row{{"state": state}}}, nil
}
func TestAwait(t *testing.T) {
	fakeClock(t)
	p := &readinessParent{ready: 3}
	out := Run(`await timeout 10s every 1s { unit status nginx |> where state == active }; echo ready`, allow(), p, Options{})
	if out.Status != 0 || out.Stdout != "ready\n" || p.n != 3 {
		t.Fatalf("%+v calls %d", out, p.n)
	}
}
func TestAwaitDeadlineAndCatch(t *testing.T) {
	now, _ := fakeClock(t)
	p := &readinessParent{ready: 99}
	out := Run("try {\n await timeout 2s every 1s { unit status nginx |> where state == active }\n} catch e { echo $e.code $e.line $e.source }", allow(), p, Options{Source: "deploy.sash"})
	if out.Status != 0 || out.Stdout != "timeout 2 deploy.sash\n" || p.n != 2 {
		t.Fatalf("%+v calls %d", out, p.n)
	}
	p = &readinessParent{ready: 1, clock: now, step: 3 * time.Second}
	out = Run(`await timeout 2s every 1s { unit status nginx |> where state == active }`, allow(), p, Options{})
	if out.Status != 2 || !strings.Contains(out.Stderr, "timeout") || p.n != 1 {
		t.Fatalf("late success: %+v calls %d", out, p.n)
	}
}
func TestAwaitRefusesMutation(t *testing.T) {
	fakeClock(t)
	for _, src := range []string{
		`await timeout 2s every 1s { unit restart nginx }`,
		`fn mutate() { unit restart nginx }; await timeout 2s every 1s { mutate }`,
		`fn mutate() { try { unit restart nginx } catch { }; return ok }; await timeout 2s every 1s { mutate }`,
		`fn mutate() { par 2 { unit restart nginx } }; await timeout 2s every 1s { mutate }`,
		`set -o plan; await timeout 2s every 1s { unit restart nginx }`,
	} {
		p := &readinessParent{}
		out := Run(src, allow(), p, Options{})
		if out.Status != 2 || p.n != 0 || !strings.Contains(out.Stderr, "await: mutation refused") {
			t.Fatalf("%s: %+v calls %d", src, out, p.n)
		}
	}
}
func TestScriptArguments(t *testing.T) {
	src := `args {
 service string required help "Unit to inspect"
 attempts int default 3
 wait duration default 30s
 enabled bool default false
 env string default dev choices "dev,prod"
 }
 echo $service $attempts $wait $enabled $env`
	out := Run(src, allow(), &fake{}, Options{Args: []string{"--service", "nginx", "--env", "prod"}})
	if out.Status != 0 || out.Stdout != "nginx 3 30s false prod\n" {
		t.Fatalf("%+v", out)
	}
	for _, args := range [][]string{nil, {"--service", "nginx", "--attempts", "oops"}, {"--service", "nginx", "--wait", "0s"}, {"--service", "nginx", "--enabled", "yes"}, {"--service", "nginx", "--env", "other"}, {"--unknown", "x"}, {"--service", "nginx", "--service", "sshd"}} {
		p := &fake{}
		out := Run(src+"\nps", allow(), p, Options{Args: args})
		if out.Status != 2 || out.Stdout != "" || len(p.calls) > 0 {
			t.Fatalf("args %q: %+v calls %v", args, out, p.calls)
		}
	}
	p := &fake{}
	out = Run(src+"\nps", allow(), p, Options{Args: []string{"--help"}})
	if out.Status != 0 || !strings.Contains(out.Stdout, "--service <string> (required)") || len(p.calls) > 0 {
		t.Fatalf("help: %+v", out)
	}
}
func TestArgumentsCannotRunSource(t *testing.T) {
	p := &fake{}
	out := Run(`args { name string required }; echo $name`, allow(), p, Options{Args: []string{"--name", "ps; unit restart nginx"}})
	if out.Status != 0 || out.Stdout != "ps; unit restart nginx\n" || len(p.calls) > 0 {
		t.Fatalf("%+v", out)
	}
	for _, src := range []string{`echo before; args { x string required }`, `fn f() { args { x string required } }`, `args { x string default $bad }`, `args { x int default invalid }`, `args { x string required; x string required }`} {
		out := Run(src, allow(), p, Options{})
		if out.Status != 2 || out.Stdout != "" {
			t.Fatalf("%s: %+v", src, out)
		}
	}
}
func TestFailureLocationInsideFunction(t *testing.T) {
	out := Run("fn fail() {\n unknown_command\n}\ntry { fail } catch e { echo $e.code $e.line $e.source }", allow(), &fake{}, Options{Source: "run.sash"})
	if out.Status != 0 || out.Stdout != "unknown_command 2 run.sash\n" {
		t.Fatalf("%+v", out)
	}
}

func TestPipelineFunctionRedirect(t *testing.T) {
	p := &fake{}
	out := Run(`fn emit() { echo saved }; emit > /var/tmp/ops/out`, allow(), p, Options{})
	if out.Status != 0 || out.Stdout != "" || len(p.calls) != 1 || string(p.calls[0].Body) != "saved\n" {
		t.Fatalf("%+v calls %+v", out, p.calls)
	}
	p = &fake{}
	out = Run(`fn read() { return $input }; read < /etc/config | egrep sshd`, allow(), p, Options{})
	if out.Status != 0 || out.Stdout != "sshd line\n" || len(p.calls) != 1 || p.calls[0].Key != "file.read" {
		t.Fatalf("%+v calls %+v", out, p.calls)
	}
}
func TestAwaitCannotDeferMutations(t *testing.T) {
	fakeClock(t)
	for _, src := range []string{
		`fn f() { atomic { echo hi > /var/tmp/ops/out } }; await timeout 1s every 1s { f }`,
		`fn f() { set -o record /var/tmp/ops/tape }; await timeout 1s every 1s { f }`,
	} {
		p := &fake{}
		out := Run(src, allow(), p, Options{})
		if out.Status != 2 || len(p.calls) != 0 {
			t.Fatalf("%s: %+v calls %+v", src, out, p.calls)
		}
	}
}
func TestAwaitCapsAndStaticTools(t *testing.T) {
	fakeClock(t)
	p := &readinessParent{ready: 99}
	out := Run(`set loopmax 1; await timeout 1h every 1ms { unit status nginx |> where state == active }`, allow(), p, Options{})
	if out.Status != 2 || p.n != 1 || !strings.Contains(out.Stderr, "loop max") {
		t.Fatalf("%+v calls %d", out, p.n)
	}
	src := `await timeout 1s every 1ms { unit status nginx }`
	n := Needs(src, NeedsOptions{})
	if !strings.Contains(n.Stdout, "unit.status") {
		t.Fatalf("needs: %+v", n)
	}
	if out := Lint(src); out.Status != 0 {
		t.Fatalf("lint: %+v", out)
	}
	if preview := Preview(src, grant.Grant{}, Options{}); !preview.Denied {
		t.Fatalf("preview: %+v", preview)
	}
}
func TestArgumentsValidatedBeforeGlobs(t *testing.T) {
	p := &fake{}
	out := Run(`args { service string required }; ls /etc/*.conf`, allow(), p, Options{})
	if out.Status != 2 || len(p.calls) != 0 {
		t.Fatalf("%+v calls %+v", out, p.calls)
	}
}

// A readiness gate that returns false never passes, whatever wraps it; a
// typed value await cannot read is a type error, never "ready".
func TestAwaitTypedReturnsFailClosed(t *testing.T) {
	fakeClock(t)
	for _, tc := range []struct {
		src    string
		status int
		stderr string
	}{
		{`fn ready() { return [ false ] }; await timeout 3s every 1s { ready }; echo proceeded`, exitType, "timeout"},
		{`fn ready() { return [ false ] }; await timeout 3s every 1s { ready | cat }; echo proceeded`, exitType, "typed value must end the condition"},
		{`fn ready() { return [ "x" ] }; await timeout 3s every 1s { ready }; echo proceeded`, exitType, "neither a bool nor rows"},
		{`fn ready() { return [ 3 ] }; await timeout 3s every 1s { ready }; echo proceeded`, exitType, "neither a bool nor rows"},
		{`fn ready() { return [ [] ] }; await timeout 3s every 1s { ready }; echo proceeded`, exitType, "timeout"},
		{`fn ready() { return [ false ] }; fn w() { echo $input }; await timeout 3s every 1s { ready | w }; echo proceeded`, exitType, "typed value must end the condition"},
		{`fn ready() { return [ [false] ] }; await timeout 3s every 1s { ready | cat }; echo proceeded`, exitType, "typed value must end the condition"},
		{`fn ready() { return [ [false, false] ] }; await timeout 3s every 1s { ready | egrep . }; echo proceeded`, exitType, "typed value must end the condition"},
		{`fn ready() { return [ [false] ] }; await timeout 3s every 1s { ready }; echo proceeded`, exitType, "neither a bool nor rows"},
		{`fn ready() { return [ {"ok": false} ] }; await timeout 3s every 1s { ready }; echo proceeded`, exitType, "returned one map"},
		{`fn ready() { return [ {"ok": false} ] }; await timeout 3s every 1s { ready | cat }; echo proceeded`, exitType, "typed value must end the condition"},
		{`fn ready() { return [ [{"ok": false}] ] }; await timeout 3s every 1s { ready |> where ok == true }; echo proceeded`, exitType, "timeout"},
		{`fn ready() { return [ true ] }; await timeout 3s every 1s { ready }; echo proceeded`, 0, ""},
		{`fn ready() { return [ [{"a": 1}] ] }; await timeout 3s every 1s { ready |> where a == 1 }; echo proceeded`, 0, ""},
	} {
		out := Run(tc.src, allow(), &fake{}, Options{})
		if out.Status != tc.status || strings.Contains(out.Stdout, "proceeded") != (tc.status == 0) || !strings.Contains(out.Stderr, tc.stderr) {
			t.Errorf("%s: %+v", tc.src, out)
		}
	}
}

// if and while read a function's returned bool by its value, as await does.
func TestConditionsReadReturnedBools(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`fn f() { return [ false ] }; if f { echo yes } else { echo no }`, "no\n"},
		{`fn f() { return [ true ] }; if f { echo yes } else { echo no }`, "yes\n"},
		{`declare -i n = 0; fn more() { return [ $n < 3 ] }; while more { let n = [ $n + 1 ] }; echo $n`, "3\n"},
	} {
		out := Run(tc.src, allow(), &fake{}, Options{})
		if out.Status != 0 || out.Stdout != tc.want {
			t.Errorf("%s: %+v want %q", tc.src, out, tc.want)
		}
	}
}

// args is static: --needs stays complete and lint finds no incomplete needs.
func TestArgumentsKeepNeedsComplete(t *testing.T) {
	src := "args { s string default x }\nps\ncat /var/log/syslog"
	n := Needs(src, NeedsOptions{})
	if n.Status != 0 || strings.Contains(n.Stdout, "not_grantable") || !strings.Contains(n.Stdout, "/var/log/syslog") {
		t.Fatalf("needs: %+v", n)
	}
	if l := Lint(src); strings.Contains(l.Stdout, "incomplete-needs") {
		t.Fatalf("lint: %+v", l)
	}
	if out := Run(`args { "a b" string default x }; echo hi`, allow(), &fake{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "not an identifier") {
		t.Fatalf("bad name: %+v", out)
	}
}

// A long await records one sleep per interval, not one per 100ms step.
func TestAwaitRecordsOneSleepPerInterval(t *testing.T) {
	rec, _ := record(t, "await timeout 10m every 5m { ps |> where pid == 999999 }")
	if n := bytes.Count(rec, []byte(`"kind":"sleep"`)); n == 0 || n > 3 {
		t.Fatalf("%d sleep events:\n%s", n, rec)
	}
}
