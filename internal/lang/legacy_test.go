package lang

import (
	"strings"
	"testing"
)

func TestAdjacentReferencesAreOneWord(t *testing.T) {
	p := &statParent{}
	for src, want := range map[string]string{
		"let a = x; let b = y; echo $a$b $a-$b pre$a": "xy x-y prex",
		"let f = app.log; cat /var/log/$f":            "/var/log/app.log",
		"let n = 3; echo v$n":                         "v3",
	} {
		out := Run(src, exprGrant(), p, Options{})
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
	// A path from a value is still checked against the roots.
	if out := Run("let f = passwd; cat /etc/$f", exprGrant(), p, Options{}); out.Status != exitDenied {
		t.Fatalf("value path: %+v", out)
	}
}

func TestWhereTakesBareComparison(t *testing.T) {
	for src, want := range map[string]string{
		"ps |> where cpu > 30 |> select cmd": "init\nsshd\nnginx",
		"ps |> where cpu < 30 |> select cmd": "zsh",
		"ps |> where pid >= 900 |> count":    "2",
	} {
		out := procs(t, src)
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
	// Elsewhere > is still a redirect: file.write is not granted here.
	if out := procs(t, "ps > /var/log/out"); out.Status != exitDenied || !strings.Contains(out.Stderr, "denied file.write") {
		t.Fatalf("redirect: %+v", out)
	}
	if out := procs(t, "ps |> where cpu > 30 > /var/log/out"); out.Status != exitDenied {
		t.Fatalf("redirect after where: %+v", out)
	}
}
