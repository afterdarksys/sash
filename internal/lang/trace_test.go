package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

func TestTraceOffByDefault(t *testing.T) {
	got := Run("echo hi", grant.Grant{}, nil, Options{})
	if got.Status != 0 || got.Stdout != "hi\n" || got.Stderr != "" {
		t.Fatalf("%+v", got)
	}
}

func TestTracePipelineAndDenied(t *testing.T) {
	p := &fake{}
	off := Run("ps | egrep sshd", grant.Grant{Entitlements: []string{"ps"}}, p, Options{})
	on := Run("ps | egrep sshd", grant.Grant{Entitlements: []string{"ps"}}, &fake{}, Options{Trace: true})
	if on.Status != 0 || on.Stdout != off.Stdout || on.Stderr != "trace ps\ntrace egrep\n" {
		t.Fatalf("on %+v off stdout %q", on, off.Stdout)
	}
	if strings.Contains(on.Stdout, "trace") {
		t.Fatalf("stdout %q", on.Stdout)
	}
	denied := Run("unit restart nginx", grant.Grant{}, &fake{}, Options{Trace: true})
	if denied.Status != 126 || denied.Stderr != "denied unit.restart\n" || strings.Contains(denied.Stderr, "trace") {
		t.Fatalf("%+v", denied)
	}
}

func TestTraceFunctionStage(t *testing.T) {
	got := Run("fn hi() {\n echo yo\n}\nhi\n", grant.Grant{}, nil, Options{Trace: true})
	if got.Status != 0 || got.Stdout != "yo\n" || got.Stderr != "trace hi\ntrace echo\n" {
		t.Fatalf("%+v", got)
	}
}

func TestTraceCommandIsNotAVerb(t *testing.T) {
	got := Run("trace on", grant.Grant{}, nil, Options{})
	if got.Status != 127 || got.Stderr != "unknown trace\n" {
		t.Fatalf("%+v", got)
	}
}

func TestTraceDoesNotPrintArgv(t *testing.T) {
	got := Run("echo secret-path", grant.Grant{}, nil, Options{Trace: true})
	if got.Stdout != "secret-path\n" || got.Stderr != "trace echo\n" {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(got.Stderr, "secret") {
		t.Fatalf("stderr %q", got.Stderr)
	}
}
