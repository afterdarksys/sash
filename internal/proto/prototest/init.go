package prototest

// Init is a test double for the init side of the verb socket
// (docs/plans/2026-11-21-sash-init-contract.md): the service verbs, a
// name-to-process table per service, and fake processes. Nothing forks.
// It re-checks each verb against its own copy of the grant, as the init
// does, so a test can show sash refusing first and the init refusing too.

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// Proc is one fake process.
type Proc struct {
	Name, Program string
	Args          []string
	Pid           int
	State         string // running, exited
	Signal        string // the signal that ended it
	Exit          int
	User, Group   string
	Env           map[string]string
	SecretEnv     []string // env names whose value came marked secret
	Opts          map[string]any
	Started       time.Time
	Ended         time.Time
}

// Init answers the service verbs for one service, Service, under G.
type Init struct {
	Service string
	G       grant.Grant
	// Procs is every service's table: service, then name. Another
	// service's names are here too, so a test can show they are refused.
	Procs map[string]map[string]*Proc
	// Status is service status by name: state.
	Status map[string]string
	// Ready holds the probe targets that are ready: "tcp 127.0.0.1:80",
	// "unix /run/x.sock", "notify main".
	Ready map[string]bool
	// Output is a process's captured lines: "name" or "name stderr".
	Output map[string][]string
	// Files is what the file verbs made: path to "dir", "file", or
	// "link <target>"; Attrs their mode and owner.
	Files  map[string]string
	Attrs  map[string]string
	Mounts map[string]string
	// Exits makes a process end on its own: name to exit status, when it
	// is spawned. Unlisted processes run until signalled.
	Exits map[string]int

	mu    sync.Mutex
	pid   int
	calls []parent.Verb
	log   []string
}

// NewInit is an Init for service under g with empty tables.
func NewInit(service string, g grant.Grant) *Init {
	return &Init{Service: service, G: g, Procs: map[string]map[string]*Proc{}, Status: map[string]string{},
		Ready: map[string]bool{}, Output: map[string][]string{}, Files: map[string]string{}, Attrs: map[string]string{},
		Mounts: map[string]string{}, Exits: map[string]int{}, pid: 1000}
}

var errInitDenied = errors.New("denied")

// Handles reports whether v is a verb Init answers.
func Handles(v parent.Verb) bool {
	if strings.HasPrefix(v.Key, "svc.") || v.Key == "fs.mount" {
		return true
	}
	if v.Key == "file.write" && len(v.Argv) > 0 {
		switch v.Argv[0] {
		case "mkdir", "chown", "chmod", "touch", "ln", "rm":
			return true
		}
	}
	return false
}

// Do performs one verb: it is a parent.Parent.
func (in *Init) Do(v parent.Verb) (parent.Result, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.calls = append(in.calls, v)
	in.log = append(in.log, v.Key+" "+strings.Join(v.Argv, " "))
	if !Handles(v) || !in.G.Has(v.Key) {
		return parent.Result{}, errInitDenied
	}
	if len(v.Argv) == 0 {
		return parent.Result{}, errInitDenied
	}
	mine := in.Procs[in.Service]
	if mine == nil {
		mine = map[string]*Proc{}
		in.Procs[in.Service] = mine
	}
	row := func(cols []string, r parent.Row) (parent.Result, error) {
		return parent.Result{Kind: parent.KindRows, Cols: cols, Rows: []parent.Row{r}}, nil
	}
	ok := parent.Result{Kind: parent.KindBytes}
	argv := v.Argv
	switch v.Key {
	case "svc.spawn":
		return in.spawn(mine, v)
	case "svc.signal":
		p := mine[argv[1]]
		if p == nil {
			return parent.Result{}, errors.New("unknown_name")
		}
		switch argv[2] {
		case "TERM", "KILL", "INT", "QUIT":
			if p.State == "running" {
				p.State, p.Signal, p.Ended = "exited", argv[2], time.Unix(1_700_000_100, 0)
			}
		}
		return ok, nil
	case "svc.wait":
		p := mine[argv[1]]
		if p == nil {
			return parent.Result{}, errors.New("unknown_name")
		}
		if p.State != "exited" {
			return parent.Result{}, errors.New("timeout")
		}
		return row([]string{"name", "exit", "signal", "ended_at"}, parent.Row{"name": p.Name, "exit": strconv.Itoa(p.Exit), "signal": p.Signal, "ended_at": p.Ended.UTC().Format(time.RFC3339)})
	case "svc.proc":
		p := mine[argv[1]]
		if p == nil {
			return parent.Result{}, errors.New("unknown_name")
		}
		return row([]string{"name", "pid", "state", "started_at"}, parent.Row{"name": p.Name, "pid": strconv.Itoa(p.Pid), "state": p.State, "started_at": p.Started.UTC().Format(time.RFC3339)})
	case "svc.output":
		if mine[argv[1]] == nil {
			return parent.Result{}, errors.New("unknown_name")
		}
		key := argv[1]
		rest := argv[2:]
		if len(rest) > 0 && rest[0] == "stderr" {
			key += " stderr"
			rest = rest[1:]
		}
		lines := in.Output[key]
		if len(rest) == 2 {
			if n, _ := strconv.Atoi(rest[1]); n < len(lines) {
				lines = lines[len(lines)-n:]
			}
		}
		res := parent.Result{Kind: parent.KindRows, Cols: []string{"line", "text"}}
		for i, l := range lines {
			res.Rows = append(res.Rows, parent.Row{"line": strconv.Itoa(i + 1), "text": l})
		}
		return res, nil
	case "svc.status":
		state, found := in.Status[argv[2]]
		if !found {
			return parent.Result{}, errors.New("unknown_service")
		}
		return row([]string{"name", "state", "since", "pid"}, parent.Row{"name": argv[2], "state": state, "since": "2026-11-23T00:00:00Z", "pid": "0"})
	case "svc.probe":
		if argv[1] == "unix" || argv[1] == "file" {
			if !in.G.AllowsRead(argv[2]) {
				return parent.Result{}, errInitDenied
			}
		}
		if in.Ready[argv[1]+" "+argv[2]] {
			return ok, nil
		}
		return parent.Result{Kind: parent.KindBytes, Exit: 1}, nil
	case "file.write":
		return in.file(v)
	case "fs.mount":
		dir := argv[len(argv)-1]
		if argv[0] == "mount" {
			dir = argv[2]
		}
		if !in.G.AllowsMount(dir) {
			return parent.Result{}, errInitDenied
		}
		if argv[0] == "mount" {
			if !in.G.AllowsMountSource(argv[1]) {
				return parent.Result{}, errInitDenied
			}
			for _, root := range in.G.FileWrite {
				if within(dir, root) || within(root, dir) {
					return parent.Result{}, errors.New("mount_overlap")
				}
			}
		}
		if argv[0] == "mount" {
			in.Mounts[dir] = argv[1]
		} else {
			delete(in.Mounts, dir)
		}
		return ok, nil
	}
	return parent.Result{}, errInitDenied
}

func (in *Init) spawn(mine map[string]*Proc, v parent.Verb) (parent.Result, error) {
	argv := v.Argv
	if len(argv) < 3 || !strings.HasPrefix(argv[2], "/") || !in.G.AllowsExec(argv[2]) {
		return parent.Result{}, errInitDenied
	}
	if p := mine[argv[1]]; p != nil && p.State == "running" {
		return parent.Result{}, errors.New("name_in_use")
	}
	var opts map[string]any
	if len(v.Body) > 0 {
		if err := json.Unmarshal(v.Body, &opts); err != nil {
			return parent.Result{}, errors.New("frame body")
		}
	}
	p := &Proc{Name: argv[1], Program: argv[2], Args: append([]string(nil), argv[3:]...), State: "running",
		Env: map[string]string{}, Opts: opts, Started: time.Unix(1_700_000_000, 0)}
	if caps, ok := opts["caps"].([]any); ok {
		for _, c := range caps {
			if s, _ := c.(string); !in.G.AllowsCap(s) {
				return parent.Result{}, errInitDenied
			}
		}
	}
	if env, ok := opts["env"].(map[string]any); ok {
		for k, e := range env {
			switch x := e.(type) {
			case string:
				p.Env[k] = x
			case map[string]any:
				s, _ := x["secret"].(string)
				p.Env[k] = s
				p.SecretEnv = append(p.SecretEnv, k)
			default:
				return parent.Result{}, errors.New("frame body")
			}
		}
	}
	// The init never trusts sash on identity or environment.
	p.User, _ = opts["user"].(string)
	p.Group, _ = opts["group"].(string)
	if p.User != in.G.User || p.Group != in.G.Group {
		return parent.Result{}, errInitDenied
	}
	for k := range p.Env {
		if refusedEnv(k) {
			return parent.Result{}, errors.New("env_refused")
		}
	}
	in.pid++
	p.Pid = in.pid
	if code, ends := in.Exits[p.Name]; ends {
		p.State, p.Exit, p.Ended = "exited", code, time.Unix(1_700_000_050, 0)
	}
	mine[p.Name] = p
	return parent.Result{Kind: parent.KindRows, Cols: []string{"name", "pid", "started_at"},
		Rows: []parent.Row{{"name": p.Name, "pid": strconv.Itoa(p.Pid), "started_at": p.Started.UTC().Format(time.RFC3339)}}}, nil
}

func (in *Init) file(v parent.Verb) (parent.Result, error) {
	argv := v.Argv
	if len(argv) < 2 {
		return parent.Result{}, errInitDenied
	}
	paths := argv[1:2]
	if argv[0] == "ln" && len(argv) == 3 {
		paths = argv[1:3]
	}
	for _, p := range paths {
		if !in.G.AllowsWrite(p) {
			return parent.Result{}, errInitDenied
		}
	}
	// No component of the path (the last included) may be a symlink:
	// the init resolves beneath the root with RESOLVE_NO_SYMLINKS, and
	// uses lchown and unlinkat. ln's TARGET is only text in the link.
	for p := argv[1]; p != "/" && p != "."; p = parentDir(p) {
		if strings.HasPrefix(in.Files[p], "link ") {
			return parent.Result{}, errors.New("symlink")
		}
	}
	ok := parent.Result{Kind: parent.KindBytes}
	path := argv[1]
	switch argv[0] {
	case "mkdir":
		if cur, found := in.Files[path]; found && cur != "dir" {
			return parent.Result{}, errors.New("exists")
		}
		in.Files[path] = "dir"
		for i := 2; i+1 < len(argv); i += 2 {
			in.Attrs[path+" "+argv[i]] = argv[i+1]
		}
	case "chown":
		in.Attrs[path+" owner"] = argv[2]
	case "chmod":
		in.Attrs[path+" mode"] = argv[2]
	case "touch":
		if _, found := in.Files[path]; !found {
			in.Files[path] = "file"
		}
	case "ln":
		in.Files[path] = "link " + argv[2]
	case "rm":
		if in.Files[path] == "dir" {
			for p := range in.Files {
				if strings.HasPrefix(p, path+"/") {
					return parent.Result{}, errors.New("not_empty")
				}
			}
		}
		delete(in.Files, path)
	}
	return ok, nil
}

// Calls is every verb the init was sent, in order.
func (in *Init) Calls() []parent.Verb {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]parent.Verb(nil), in.calls...)
}

// Log is the init's audit lines: key and argv, never an env value.
func (in *Init) Log() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.log...)
}

// Proc is this service's process name, or nil.
func (in *Init) Proc(name string) *Proc {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.Procs[in.Service][name]
}

func refusedEnv(k string) bool {
	for _, p := range []string{"LD_", "DYLD_", "MALLOC_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	switch k {
	case "GCONV_PATH", "LOCPATH", "NLSPATH", "HOSTALIASES", "RES_OPTIONS", "GLIBC_TUNABLES":
		return true
	}
	return false
}

// within is p equal to or beneath root, by whole components.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}
