package main

// sash --record and --replay (docs/plans/2026-11-12-sash-v6-tier3.md §4),
// offline only. Recording runs against a fixture: a local JSON file
// that names a grant and canned results, served by a fake parent, so no
// host is reached and nothing is written but the recording itself. That
// is the simplest safe source: a session's parent would be a real host,
// and in-session recording must write through file.write with a mode
// (jobs §2), which does not exist yet. Replay takes the same fixture for
// its grant only; every result comes from the recording, and its parent
// refuses every call. A recording never authorizes anything.
//
//	sash --record out.jsonl --fixture fixture.json [--expires 168h] [-c script | file]
//	sash --replay rec.jsonl --fixture fixture.json [-c script | file]

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/line"
	"github.com/afterdarksys/sash/internal/parent"
)

const (
	tapeUsage   = "usage: sash --record out.jsonl --fixture fixture.json [--expires D] [-c script | file]; sash --replay rec.jsonl --fixture fixture.json [-c script | file]"
	fixtureMax  = 8 << 20
	expiresMax  = 365 * 24 * time.Hour
	expiresDflt = 7 * 24 * time.Hour
)

// tapeMode is true when --record or --replay is any argument: runTape
// then owns the whole command line and refuses anything else.
func tapeMode(args []string) bool {
	for _, a := range args {
		if a == "--record" || a == "--replay" {
			return true
		}
	}
	return false
}

type fixtureResult struct {
	Key   string       `json:"key"`
	Argv  []string     `json:"argv,omitempty"`
	Kind  string       `json:"kind,omitempty"`
	Cols  []string     `json:"cols,omitempty"`
	Rows  []parent.Row `json:"rows,omitempty"`
	Bytes string       `json:"bytes,omitempty"`
	Exit  int          `json:"exit,omitempty"`
	Error string       `json:"error,omitempty"`
}

type fixture struct {
	Grant struct {
		Entitlements []string `json:"entitlements"`
		FileRead     []string `json:"file_read"`
		FileWrite    []string `json:"file_write"`
	} `json:"grant"`
	Results []fixtureResult `json:"results"`
}

// fixtureParent answers each call with the first result whose key (and
// argv, when the result names one) matches; anything else fails.
type fixtureParent struct{ results []fixtureResult }

func (f fixtureParent) Do(v parent.Verb) (parent.Result, error) {
	for _, r := range f.results {
		if r.Key != v.Key || (r.Argv != nil && strings.Join(r.Argv, "\x00") != strings.Join(v.Argv, "\x00")) {
			continue
		}
		if r.Error != "" {
			return parent.Result{}, errors.New(r.Error)
		}
		res := parent.Result{Kind: parent.KindBytes, Bytes: []byte(r.Bytes), Exit: r.Exit, Cols: r.Cols, Rows: r.Rows}
		if r.Kind == "rows" {
			res.Kind = parent.KindRows
		}
		return res, nil
	}
	return parent.Result{}, fmt.Errorf("fixture: no result for %s", v.Key)
}

// loadFixture reads a fixture: strict JSON, at most 8 MiB, entitlements
// without "*", and clean absolute roots that are not "/", as a signed
// grant must be.
func loadFixture(p string) (fixture, grant.Grant, error) {
	var fx fixture
	f, err := os.Open(p)
	if err != nil {
		return fx, grant.Grant{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, fixtureMax+1))
	if err != nil {
		return fx, grant.Grant{}, err
	}
	if len(b) > fixtureMax {
		return fx, grant.Grant{}, errors.New("fixture too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fx); err != nil {
		return fx, grant.Grant{}, errors.New("fixture: bad shape")
	}
	for _, e := range fx.Grant.Entitlements {
		if e == "" || e == "*" {
			return fx, grant.Grant{}, errors.New("fixture: bad entitlement")
		}
	}
	for _, r := range append(append([]string(nil), fx.Grant.FileRead...), fx.Grant.FileWrite...) {
		if !strings.HasPrefix(r, "/") || path.Clean(r) != r || r == "/" {
			return fx, grant.Grant{}, errors.New("fixture: bad root")
		}
	}
	g := grant.Grant{V: "sash/1", Purpose: "offline", Entitlements: fx.Grant.Entitlements, FileRead: fx.Grant.FileRead, FileWrite: fx.Grant.FileWrite}
	return fx, g, nil
}

// runTape is --record and --replay.
func runTape(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, tapeUsage)
		return 2
	}
	mode, recPath, fxPath := "", "", ""
	expires := expiresDflt
	for len(args) >= 2 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--record", "--replay":
			if mode != "" {
				return usage()
			}
			mode, recPath = args[0], args[1]
		case "--fixture":
			if fxPath != "" {
				return usage()
			}
			fxPath = args[1]
		case "--expires":
			d, err := time.ParseDuration(args[1])
			if err != nil || d < time.Minute || d > expiresMax {
				return usage()
			}
			expires = d
		default:
			return usage()
		}
		args = args[2:]
	}
	if mode == "" || fxPath == "" || recPath == "" || strings.HasPrefix(recPath, "-") || strings.HasPrefix(fxPath, "-") {
		return usage()
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "--") {
		return usage()
	}
	if len(args) == 0 && line.IsTerm(os.Stdin) {
		return usage()
	}
	src, err := readSource(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		return 2
	}
	fx, g, err := loadFixture(fxPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		return 2
	}
	if mode == "--record" {
		return record(src, recPath, g, fixtureParent{fx.Results}, expires)
	}
	return replay(src, recPath, g)
}

// atomicFile publishes the recording in one step. Its one Write goes to
// a new temp file in the same directory (O_EXCL, 0600), is synced, and is
// then hard-linked to the final name, which link refuses if it exists;
// the temp name is removed after. A kill at any point leaves at most the
// temp file, never a truncated recording at the final path. The tape
// writes once, at Finish, so a run killed before then leaves nothing.
type atomicFile struct {
	path    string
	written bool
	// beforeLink runs after the sync and before the link; tests use it.
	beforeLink func(tmp string) error
}

func (a *atomicFile) Write(b []byte) (int, error) {
	if a.written {
		return 0, errors.New("record: one write only")
	}
	a.written = true
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return 0, err
	}
	tmp := filepath.Join(filepath.Dir(a.path), "."+filepath.Base(a.path)+".tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	fail := func(err error) (int, error) {
		if cerr := f.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			err = errors.Join(err, cerr)
		}
		if rerr := os.Remove(tmp); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return 0, err
	}
	if _, err := f.Write(b); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if a.beforeLink != nil {
		if err := a.beforeLink(tmp); err != nil {
			return 0, err
		}
	}
	if err := os.Link(tmp, a.path); err != nil {
		if rerr := os.Remove(tmp); rerr != nil {
			err = errors.Join(err, rerr)
		}
		return 0, err
	}
	if err := os.Remove(tmp); err != nil {
		return 0, err
	}
	return len(b), nil
}

func record(src, recPath string, g grant.Grant, p parent.Parent, expires time.Duration) int {
	if _, err := os.Lstat(recPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "record_exists")
		return 2
	}
	out := &atomicFile{path: recPath}
	tape, err := lang.NewRecorder(out, src, time.Now().Add(expires))
	if err != nil {
		fmt.Fprintln(os.Stderr, "record_failed")
		return 2
	}
	got := lang.Run(src, g, p, lang.Options{Tape: tape})
	fmt.Fprint(os.Stdout, got.Stdout)
	fmt.Fprint(os.Stderr, got.Stderr)
	if err := tape.Finish(); err != nil {
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		return 2
	}
	return langExit(got.Status)
}

func replay(src, recPath string, g grant.Grant) int {
	f, err := os.Open(recPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		return 2
	}
	defer f.Close()
	tape, err := lang.LoadReplay(f, src, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		return 2
	}
	got := lang.Run(src, g, closedParent{}, lang.Options{Tape: tape})
	fmt.Fprint(os.Stdout, got.Stdout)
	fmt.Fprint(os.Stderr, got.Stderr)
	if tape.Err() != nil {
		return 2
	}
	if err := tape.Finish(); err != nil {
		fmt.Fprintln(os.Stderr, lang.SafeText(err.Error()))
		return 2
	}
	return langExit(got.Status)
}
