package main

// sash version --json and sash codes --json: what this build is, and the
// catalog of what it prints when it refuses or fails (package codes).
//
// Threats: catalog text read by an agent as an instruction (the fix-text
// rule and the reviewed fixture, in package codes), a catalog that drifts
// from the binary (the scan test there), and version output used to gate
// anything: it is a claim a binary makes about itself, for display only.
// Both modes are recognised only as exactly two arguments; anything else
// with them is the usage line and exit 2. They read no grant, call no
// parent, open no file, and never touch the session descriptors.

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/afterdarksys/sash/internal/codes"
	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/proto"
)

const infoUsage = "usage: sash version --json; sash codes --json"

// infoMode is true when the first argument is version or codes followed
// by a flag, or the only argument: runInfo then owns the command line. A
// file named version or codes is still a script when it stands alone with
// no flag after it, as before.
func infoMode(args []string) bool {
	if len(args) < 2 || (args[0] != "version" && args[0] != "codes") {
		return false
	}
	return strings.HasPrefix(args[1], "-")
}

type versionDoc struct {
	V           int    `json:"v"`
	OK          bool   `json:"ok"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Modified    bool   `json:"modified"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Grant       string `json:"grant"`
	Mux         int    `json:"mux"`
	Wasm        bool   `json:"wasm"`
	Forms       int    `json:"forms"`
	Codes       int    `json:"codes"`
	CodesSHA256 string `json:"codes_sha256"`
}

type codesDoc struct {
	V       int          `json:"v"`
	OK      bool         `json:"ok"`
	Code    string       `json:"code"`
	Name    string       `json:"name"`
	Version string       `json:"version"`
	Codes   []codes.Code `json:"codes"`
}

// buildInfo is the module version, the commit, and whether the tree was
// modified, from the binary's own build information.
func buildInfo() (version, commit string, modified bool) {
	version, commit = "unknown", "unknown"
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version, commit, false
	}
	if bi.Main.Version != "" {
		version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				commit = s.Value
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return version, commit, modified
}

// runInfo prints one JSON object on one line.
func runInfo(args []string, out, errOut io.Writer) int {
	if len(args) != 2 || args[1] != "--json" {
		fmt.Fprintln(errOut, infoUsage)
		return 2
	}
	version, commit, modified := buildInfo()
	var doc any
	if args[0] == "version" {
		sum, err := codes.SHA256()
		if err != nil {
			fmt.Fprintln(errOut, "codes: catalog unreadable")
			return 1
		}
		doc = versionDoc{
			V: 1, OK: true, Code: "ok", Name: "sash", Version: version, Commit: commit, Modified: modified,
			OS: runtime.GOOS, Arch: runtime.GOARCH, Grant: grant.Version, Mux: proto.MuxVersion, Wasm: lang.WasmBuilt(),
			Forms: len(lang.Builtins()), Codes: len(codes.All()), CodesSHA256: sum,
		}
	} else {
		doc = codesDoc{V: 1, OK: true, Code: "ok", Name: "sash", Version: version, Codes: codes.All()}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		fmt.Fprintln(errOut, "codes: catalog unreadable")
		return 1
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return 1
	}
	return 0
}
