package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/codes"
	"github.com/afterdarksys/sash/internal/lang"
)

// sash version --json prints one line, one object, with exactly the
// fields the contract names.
func TestVersionJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runInfo([]string{"version", "--json"}, &out, &errb); code != 0 || errb.Len() != 0 {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.HasSuffix(out.String(), "}\n") {
		t.Fatalf("not one line: %q", out.String())
	}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	dec.DisallowUnknownFields()
	var v versionDoc
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	sum, err := codes.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	if v.V != 1 || !v.OK || v.Code != "ok" || v.Name != "sash" || v.Version == "" || v.Commit == "" ||
		v.OS != runtime.GOOS || v.Arch != runtime.GOARCH || v.Grant != "sash/1" || v.Mux != 1 || v.Wasm != lang.WasmBuilt() ||
		v.Forms != len(lang.Builtins()) || v.Codes != len(codes.All()) || v.CodesSHA256 != sum {
		t.Errorf("version: %+v", v)
	}
	var keys []string
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if got := strings.Join(keys, " "); got != "arch code codes codes_sha256 commit forms grant modified mux name ok os v version wasm" {
		t.Errorf("fields %q", got)
	}
}

// sash codes --json prints the catalog, sorted by code, each entry with
// its five fields, and its bytes are what version's codes_sha256 covers.
func TestCodesJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runInfo([]string{"codes", "--json"}, &out, &errb); code != 0 || errb.Len() != 0 {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("not one line")
	}
	var doc struct {
		V       int                 `json:"v"`
		OK      bool                `json:"ok"`
		Code    string              `json:"code"`
		Name    string              `json:"name"`
		Version string              `json:"version"`
		Codes   json.RawMessage     `json:"codes"`
		Extra   map[string]struct{} `json:"-"`
	}
	dec := json.NewDecoder(strings.NewReader(out.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.V != 1 || !doc.OK || doc.Code != "ok" || doc.Name != "sash" || doc.Version == "" {
		t.Errorf("header %+v", doc)
	}
	var list []map[string]any
	if err := json.Unmarshal(doc.Codes, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != len(codes.All()) || len(list) < 60 {
		t.Fatalf("%d codes", len(list))
	}
	prev := ""
	for _, c := range list {
		if len(c) != 5 {
			t.Errorf("entry %v has %d fields", c["code"], len(c))
		}
		for _, k := range []string{"code", "exit", "area", "meaning", "fix"} {
			if _, ok := c[k]; !ok {
				t.Errorf("entry %v lacks %s", c["code"], k)
			}
		}
		code, _ := c["code"].(string)
		if code <= prev {
			t.Errorf("%q after %q: not sorted", code, prev)
		}
		prev = code
	}
	sum := sha256.Sum256(doc.Codes)
	want, err := codes.SHA256()
	if err != nil || hex.EncodeToString(sum[:]) != want {
		t.Errorf("codes_sha256 does not cover the printed codes")
	}
	// The text is not HTML-escaped: |> reads as written.
	if !strings.Contains(out.String(), "|>") || !strings.Contains(out.String(), "<code>") || strings.Contains(out.String(), "\\u00"+"3e") || strings.Contains(out.String(), "\\u00"+"3c") {
		t.Errorf("escaped < or > in %q", out.String()[:200])
	}
}

// Both modes are exactly two arguments. Anything else with them is the
// usage line and exit 2, and nothing is printed on stdout.
func TestInfoModesRefuseEverythingElse(t *testing.T) {
	for _, args := range [][]string{
		{"version", "--json", "x"}, {"version", "--json", "--json"}, {"codes", "--json", "codes"}, {"version", "--text"},
		{"codes", "--plan"}, {"version", "--session"}, {"codes", "-c"}, {"version", "--json", "--plan"}, {"codes", "--"},
	} {
		if !infoMode(args) {
			t.Errorf("%q: not taken as an info mode", args)
			continue
		}
		var out, errb bytes.Buffer
		if code := runInfo(args, &out, &errb); code != 2 || out.Len() != 0 || errb.String() != infoUsage+"\n" {
			t.Errorf("%q: code %d stdout %q stderr %q", args, code, out.String(), errb.String())
		}
	}
	// These are not info modes: a script named version, and flags first.
	for _, args := range [][]string{{"version"}, {"codes"}, {"version", "x"}, {"--plan", "version", "--json"}, {"--json", "version"}, {"-c", "version --json"}, {}} {
		if infoMode(args) {
			t.Errorf("%q: taken as an info mode", args)
		}
	}
}

// The binary: both modes work with every other descriptor closed and an
// empty environment, so they read no grant and open nothing; a wrong form
// is exit 2; and a script file named version is still a script.
func TestBinaryVersionAndCodes(t *testing.T) {
	bin := ptyBin(t)
	run := func(dir string, args ...string) (string, string, int) {
		t.Helper()
		var out, errb bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{}
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = &out, &errb
		code := exitCode(cmd.Run())
		return out.String(), errb.String(), code
	}
	empty := t.TempDir()
	out, errb, code := run(empty, "version", "--json")
	var v versionDoc
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || errb != "" || v.Name != "sash" || v.Forms != len(lang.Builtins()) {
		t.Errorf("version: code %d %q %q %v", code, out, errb, err)
	}
	out, errb, code = run(empty, "codes", "--json")
	if code != 0 || errb != "" || !strings.Contains(out, `"code":"denied","exit":126`) {
		t.Errorf("codes: code %d stderr %q", code, errb)
	}
	for _, args := range [][]string{{"version", "--json", "x"}, {"codes", "--json", "--plan"}, {"version", "--help"}, {"--plan", "version", "--json"}, {"--session", "codes", "--json"}, {"version", "x"}} {
		out, errb, code = run(empty, args...)
		if code != 2 || out != "" || !strings.Contains(errb, "usage") {
			if args[0] == "--session" && code == 2 && out == "" {
				continue // refused at the grant, before any script: still exit 2, nothing printed
			}
			t.Errorf("%q: code %d stdout %q stderr %q", args, code, out, errb)
		}
	}
	// A script file named version runs as a script, as it did before.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte("echo a script\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, code = run(dir, "version"); code != 0 || out != "a script\n" {
		t.Errorf("a script named version: code %d %q", code, out)
	}
}
