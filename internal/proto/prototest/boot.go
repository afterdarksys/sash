package prototest

// The init double's boot verbs (docs/plans/2026-11-24-sash-boot-verbs-
// contract.md): module load, device wait, mount ensure, network apply,
// service start and service stop, against a host.json and a fake kernel.
//
// Threats: the double stands for the init, which never trusts sash. Each
// boot verb is re-checked here in the contract's order: the key in the
// grant, a sash/3 boot grant bound to this host, the unit's opt-in, the
// start entry, the boot window, the per-boot budget, and no field beside
// Key and Argv; then the name in the grant's list and every per-verb rule
// against root's declarations. A name never becomes a path here except
// through host.json or modules.dep, and a modules.dep path that is
// absolute, unclean or climbs is refused. Nothing touches the real host:
// the kernel, /dev, the mounts, the links and the catalog are tables.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// HostFixture is the host.json the boot tests run against.
const HostFixture = `{
  "devices": {
    "data": {"path": "/dev/vdb", "kind": "block", "match": {"serial": "DATA0001", "size": "2097152"}},
    "logs": {"path": "/dev/disk/by-id/logs", "kind": "block", "match": {"wwid": "naa.5000c500"}}
  },
  "mounts": {
    "data": {"target": "/data", "fstype": "ext4", "source": "data", "options": ["noatime"]},
    "scratch": {"target": "/scratch", "fstype": "tmpfs", "source": "tmpfs", "options": ["size=64m", "mode=0755"]}
  },
  "network": {
    "lan": {"kind": "static", "link": "52:54:00:12:34:56", "mtu": 1500,
      "addresses": ["10.0.2.15/24"], "routes": [{"dst": "0.0.0.0/0", "gw": "10.0.2.2"}]},
    "wan": {"kind": "static", "link": "52:54:00:aa:bb:cc", "addresses": ["203.0.113.7/24"], "requires": ["firewall"]},
    "dhcp": {"kind": "unit", "unit": "dhcp"}
  }
}`

// Host declarations, as sahinit reads host.json.
type HostDecl struct {
	Devices map[string]DeviceDecl  `json:"devices"`
	Mounts  map[string]MountDecl   `json:"mounts"`
	Network map[string]NetworkDecl `json:"network"`
}

type DeviceDecl struct {
	Path  string            `json:"path"`
	Kind  string            `json:"kind"`
	Match map[string]string `json:"match"`
}

type MountDecl struct {
	Target  string   `json:"target"`
	FSType  string   `json:"fstype"`
	Source  string   `json:"source"`
	Options []string `json:"options"`
}

type NetworkDecl struct {
	Kind      string      `json:"kind"`
	Link      string      `json:"link"`
	Name      string      `json:"name"`
	MTU       int         `json:"mtu"`
	Addresses []string    `json:"addresses"`
	Routes    []RouteDecl `json:"routes"`
	Requires  []string    `json:"requires"`
	Unit      string      `json:"unit"`
}

type RouteDecl struct {
	Dst string `json:"dst"`
	GW  string `json:"gw"`
}

const hostDeclMax = 64 * 1024

var (
	errHostDecl  = errors.New("host_decl_invalid")
	matchAttrs   = map[string]bool{"serial": true, "wwid": true, "device/serial": true, "device/wwid": true, "size": true, "partition": true, "dm/uuid": true}
	hostFSTypes  = map[string]bool{"ext4": true, "xfs": true, "vfat": true, "tmpfs": true}
	hostMountOpt = map[string]bool{"ro": true, "rw": true, "noexec": true, "noatime": true, "relatime": true, "nosuid": true, "nodev": true, "suid": true, "dev": true}
	macRe        = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)
	ifNameRe     = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,14}$`)
	devPathRe    = regexp.MustCompile(`^/dev/[a-zA-Z0-9._/-]+$`)
	// optConflict are option pairs a declaration may not hold both of.
	optConflict = [][2]string{{"ro", "rw"}, {"suid", "nosuid"}, {"dev", "nodev"}}
)

// ParseHostDecl reads host.json strictly: closed key sets, at most 64 KiB
// and 64 entries per table, every name in its grammar, devices under /dev
// with a known kind and at most 8 known match attributes, mounts of an
// allowed fstype from a declared device (or tmpfs) with allowlisted
// options and no shared target, and network profiles that are static or
// unit; a static profile with a non-private address must list requires.
func ParseHostDecl(doc []byte) (HostDecl, error) {
	var h HostDecl
	if len(doc) > hostDeclMax {
		return h, errHostDecl
	}
	// encoding/json matches keys without case and keeps the last of a
	// duplicate, so the document is walked first: every key exact and
	// once, no null anywhere, one value and nothing after it.
	if err := strictHostJSON(doc); err != nil {
		return HostDecl{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return HostDecl{}, errHostDecl
	}
	if _, err := dec.Token(); err != io.EOF {
		return HostDecl{}, errHostDecl
	}
	if len(h.Devices) > 64 || len(h.Mounts) > 64 || len(h.Network) > 64 {
		return HostDecl{}, errHostDecl
	}
	for name, d := range h.Devices {
		if !grant.BootName.MatchString(name) || path.Clean(d.Path) != d.Path || !devPathRe.MatchString(d.Path) ||
			(d.Kind != "block" && d.Kind != "char") || len(d.Match) > 8 {
			return HostDecl{}, errHostDecl
		}
		for a, v := range d.Match {
			if !matchAttrs[a] || v == "" || len(v) > 256 {
				return HostDecl{}, errHostDecl
			}
		}
	}
	targets := map[string]bool{}
	for name, m := range h.Mounts {
		if !grant.BootName.MatchString(name) || !hostFSTypes[m.FSType] || !strings.HasPrefix(m.Target, "/") ||
			path.Clean(m.Target) != m.Target || m.Target == "/" || targets[m.Target] {
			return HostDecl{}, errHostDecl
		}
		targets[m.Target] = true
		if (m.Source == "tmpfs") != (m.FSType == "tmpfs") {
			return HostDecl{}, errHostDecl
		}
		if _, ok := h.Devices[m.Source]; m.Source != "tmpfs" && !ok {
			return HostDecl{}, errHostDecl
		}
		for _, pair := range optConflict {
			if slices.Contains(m.Options, pair[0]) && slices.Contains(m.Options, pair[1]) {
				return HostDecl{}, errHostDecl
			}
		}
		for _, o := range m.Options {
			k, v, val := strings.Cut(o, "=")
			switch {
			case !val && hostMountOpt[o]:
			case val && m.FSType == "tmpfs" && (k == "size" || k == "mode"):
				if _, err := grant.CheckMountOpts("tmpfs", "tmpfs", []string{k + "=" + v}); err != nil {
					return HostDecl{}, errHostDecl
				}
			default:
				return HostDecl{}, errHostDecl
			}
		}
	}
	for name, n := range h.Network {
		if !grant.BootName.MatchString(name) {
			return HostDecl{}, errHostDecl
		}
		switch n.Kind {
		case "unit":
			if !grant.UnitName.MatchString(n.Unit) || n.Link != "" || len(n.Addresses)+len(n.Routes)+len(n.Requires) > 0 {
				return HostDecl{}, errHostDecl
			}
		case "static":
			if !macRe.MatchString(n.Link) || (n.Name != "" && !ifNameRe.MatchString(n.Name)) || n.Unit != "" ||
				len(n.Addresses) > 8 || len(n.Routes) > 16 || n.MTU < 0 || n.MTU > 65535 {
				return HostDecl{}, errHostDecl
			}
			public := false
			for _, a := range n.Addresses {
				p, err := netip.ParsePrefix(a)
				if err != nil {
					return HostDecl{}, errHostDecl
				}
				if !p.Addr().IsPrivate() && !p.Addr().IsLoopback() && !p.Addr().IsLinkLocalUnicast() {
					public = true
				}
			}
			for _, r := range n.Routes {
				if _, err := netip.ParsePrefix(r.Dst); err != nil {
					return HostDecl{}, errHostDecl
				}
				if _, err := netip.ParseAddr(r.GW); err != nil {
					return HostDecl{}, errHostDecl
				}
			}
			for _, u := range n.Requires {
				if !grant.UnitName.MatchString(u) {
					return HostDecl{}, errHostDecl
				}
			}
			// Decision 5: a public address goes live only behind a unit.
			if public && len(n.Requires) == 0 {
				return HostDecl{}, errHostDecl
			}
		default:
			return HostDecl{}, errHostDecl
		}
	}
	return h, nil
}

// hostSchema is host.json's shape for strictHostJSON: an object's exact
// keys, a table (name to entry), a list, or a scalar.
type hostSchema struct {
	fields map[string]*hostSchema // an object
	table  *hostSchema            // a map of names to this
	list   *hostSchema            // an array of this
	scalar bool                   // a string, number or bool
}

var (
	hsScalar      = &hostSchema{scalar: true}
	hsStrings     = &hostSchema{list: hsScalar}
	hostSchemaTop = &hostSchema{fields: map[string]*hostSchema{
		"devices": {table: &hostSchema{fields: map[string]*hostSchema{
			"path": hsScalar, "kind": hsScalar, "match": {table: hsScalar},
		}}},
		"mounts": {table: &hostSchema{fields: map[string]*hostSchema{
			"target": hsScalar, "fstype": hsScalar, "source": hsScalar, "options": hsStrings,
		}}},
		"network": {table: &hostSchema{fields: map[string]*hostSchema{
			"kind": hsScalar, "link": hsScalar, "name": hsScalar, "mtu": hsScalar, "addresses": hsStrings,
			"routes":   {list: &hostSchema{fields: map[string]*hostSchema{"dst": hsScalar, "gw": hsScalar}}},
			"requires": hsStrings, "unit": hsScalar,
		}}},
	}}
)

// strictHostJSON walks doc token by token against hostSchemaTop: every
// key an exact, case-sensitive match, none twice in one object, no null at
// any level (the top included), and nothing after the document.
func strictHostJSON(doc []byte) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var walk func(s *hostSchema, depth int) error
	walk = func(s *hostSchema, depth int) error {
		if depth > 8 {
			return errHostDecl
		}
		tok, err := dec.Token()
		if err != nil || tok == nil {
			return errHostDecl
		}
		switch t := tok.(type) {
		case json.Delim:
			switch {
			case t == '{' && (s.fields != nil || s.table != nil):
				seen := map[string]bool{}
				for dec.More() {
					kt, err := dec.Token()
					k, ok := kt.(string)
					if err != nil || !ok || seen[k] {
						return errHostDecl
					}
					seen[k] = true
					next := s.table
					if s.fields != nil {
						next = s.fields[k]
					}
					if next == nil {
						return errHostDecl
					}
					if err := walk(next, depth+1); err != nil {
						return err
					}
				}
			case t == '[' && s.list != nil:
				for dec.More() {
					if err := walk(s.list, depth+1); err != nil {
						return err
					}
				}
			default:
				return errHostDecl
			}
			if _, err := dec.Token(); err != nil {
				return errHostDecl
			}
			return nil
		default:
			if !s.scalar {
				return errHostDecl
			}
			return nil
		}
	}
	if err := walk(hostSchemaTop, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errHostDecl
	}
	return nil
}

// FakeDev is one node under /dev and its sysfs attributes.
type FakeDev struct {
	Kind         string // block or char
	Major, Minor int
	Attrs        map[string]string
	// Symlink marks a node (or a component on its way) that is a link.
	Symlink bool
}

// FakeLink is one network link.
type FakeLink struct {
	MAC, Name string
	MTU       int
	Up        bool
	Addrs     []string
	Routes    []RouteDecl
}

// HostMount is one host mount the double made or found.
type HostMount struct {
	FSType string
	Dev    string // "MAJ:MIN", "" for tmpfs
	RO     bool
	Opts   []string
}

// Unit is one catalog unit.
type Unit struct {
	Boot, BootOps, Essential bool
	Needs, After             []string
	State                    string // stopped, running, exited, ready
	// Exit is a oneshot's status once it has exited.
	Exit int
}

// Boot is the boot side of the double: host.json, the fake kernel and the
// catalog. Init.Boot is nil until a test sets one; every boot verb is then
// denied.
type Boot struct {
	// HostID is the boot-policy trust file's host_id; "" is no trust file,
	// and every sash/3 grant is then sash_grant_host.
	HostID string
	// OptedIn is the unit's "boot_ops": true; Entry the session's entry;
	// Booted the boot waves' end.
	OptedIn bool
	Entry   string
	Booted  bool
	Host    HostDecl

	// The fake kernel.
	ModulesDisabled bool
	SigEnforce      bool
	Lockdown        bool
	// UnsignedDev is a binary built with sahinit_unsigned_modules.
	UnsignedDev bool
	// ModulesDep is modules.dep's text; Loaded the live modules; LoadFail
	// modules the kernel refuses.
	ModulesDep string
	Loaded     map[string]bool
	LoadFail   map[string]bool
	// LoadOrder is every module the double loaded, in order.
	LoadOrder []string
	DevTmpfs  bool
	Devs      map[string]*FakeDev
	// Dirs are existing directories: path to "root 0755"-style owner and
	// mode.
	Dirs       map[string]string
	HostMounts map[string]*HostMount
	Links      []*FakeLink
	Units      map[string]*Unit

	calls   int
	waited  map[string]string // device name to MAJ:MIN
	touched map[string]bool   // units started or stopped by a boot verb
	// Logs are the double's own log lines (module_unsigned_dev).
	Logs []string
}

// NewBoot is a Boot on the fixture host: the right host, opted in, entry
// start, not booted, signature enforcement on, devtmpfs, the data disk
// present, /data and /scratch root-owned directories, the lan link, and
// a catalog with dhcp, firewall, getty-tty1, an essential sshd and the
// boot-ops unit itself.
func NewBoot(service string) *Boot {
	h, err := ParseHostDecl([]byte(HostFixture))
	if err != nil {
		panic(err)
	}
	return &Boot{
		HostID: "web1", OptedIn: true, Entry: "start", Host: h, SigEnforce: true, DevTmpfs: true,
		ModulesDep: "kernel/drivers/block/virtio_blk.ko.zst: kernel/drivers/virtio/virtio_ring.ko kernel/drivers/virtio/virtio.ko.xz\n" +
			"kernel/drivers/net/virtio_net.ko.zst: kernel/drivers/net/net_failover.ko kernel/net/core/failover.ko kernel/drivers/virtio/virtio_ring.ko kernel/drivers/virtio/virtio.ko.xz\n" +
			"kernel/drivers/virtio/virtio_ring.ko:\nkernel/drivers/virtio/virtio.ko.xz:\nkernel/net/core/failover.ko:\nkernel/drivers/net/net_failover.ko: kernel/net/core/failover.ko\n" +
			"kernel/fs/ext4/ext4.ko.gz: kernel/fs/jbd2/jbd2.ko\nkernel/fs/jbd2/jbd2.ko:\n",
		Loaded: map[string]bool{}, LoadFail: map[string]bool{},
		Devs:       map[string]*FakeDev{"/dev/vdb": {Kind: "block", Major: 253, Minor: 16, Attrs: map[string]string{"serial": "DATA0001", "size": "2097152"}}},
		Dirs:       map[string]string{"/data": "root 0755", "/scratch": "root 0755"},
		HostMounts: map[string]*HostMount{},
		Links:      []*FakeLink{{MAC: "52:54:00:12:34:56", Name: "eth0", MTU: 1500}},
		Units: map[string]*Unit{
			"dhcp": {State: "stopped"}, "firewall": {State: "stopped"}, "getty-tty1": {State: "running"},
			"sshd": {Boot: true, Essential: true, State: "running"}, "app": {Boot: true, Needs: []string{service}, State: "stopped"},
			"otherprep": {Boot: true, BootOps: true, State: "exited"}, "late": {After: []string{service}, State: "stopped"},
			service: {Boot: true, BootOps: true, State: "running"},
		},
		waited: map[string]string{}, touched: map[string]bool{},
	}
}

func code(c string) error { return errors.New(c) }

// boot re-checks and performs one boot verb. The caller holds in.mu.
func (in *Init) boot(v parent.Verb) (parent.Result, error) {
	b := in.Boot
	g := in.G
	// 1 the key; 2 a sash/3 boot grant for this host; 3 the opt-in; 4 the
	// entry; 5 the window; 6 the budget; 7 no field beside Key and Argv.
	if b == nil || !g.Has(v.Key) {
		return parent.Result{}, errInitDenied
	}
	if !g.BootOps() || g.Service != in.Service {
		return parent.Result{}, errInitDenied
	}
	if b.HostID == "" || g.Host != b.HostID {
		return parent.Result{}, code("sash_grant_host")
	}
	if !b.OptedIn {
		return parent.Result{}, code("sash_boot_ops")
	}
	if b.Entry != "start" {
		return parent.Result{}, code("boot_entry")
	}
	if b.Booted {
		return parent.Result{}, code("boot_over")
	}
	argv := v.Argv
	shape, known := map[string]struct {
		verb, sub string
		n         int
		re        *regexp.Regexp
		allows    func(string) bool
	}{
		// The lists themselves: gate 2 above is the one sash/3 check, so
		// a test can show it holds on its own.
		"boot.module": {"module", "load", 3, grant.ModuleName, listed(g.Modules)},
		"boot.device": {"device", "wait", 5, grant.BootName, listed(g.Devices)},
		"boot.mount":  {"mount", "ensure", 3, grant.BootName, listed(g.HostMounts)},
		"boot.net":    {"network", "apply", 3, grant.BootName, listed(g.NetProfiles)},
		"boot.start":  {"service", "start", 3, grant.UnitName, listed(g.UnitsStart)},
		"boot.stop":   {"service", "stop", 3, grant.UnitName, listed(g.UnitsStop)},
	}[v.Key]
	// An unknown boot key is denied before the budget is charged.
	if !known {
		return parent.Result{}, errInitDenied
	}
	if b.calls >= grant.MaxBootList {
		return parent.Result{}, code("boot_call_limit")
	}
	b.calls++
	if v.Path != "" || len(v.Body) != 0 || v.Mode != "" {
		return parent.Result{}, errInitDenied
	}
	if len(argv) != shape.n || argv[0] != shape.verb || argv[1] != shape.sub || !shape.re.MatchString(argv[2]) || !shape.allows(argv[2]) {
		return parent.Result{}, errInitDenied
	}
	name := argv[2]
	switch v.Key {
	case "boot.module":
		return b.loadModule(name)
	case "boot.device":
		secs, err := strconv.Atoi(argv[4])
		if argv[3] != "timeout" || err != nil || secs < 1 || secs > 60 || strconv.Itoa(secs) != argv[4] {
			return parent.Result{}, errInitDenied
		}
		return b.waitDevice(name)
	case "boot.mount":
		return b.ensureMount(name, g)
	case "boot.net":
		return b.applyNetwork(name, in.Service)
	case "boot.start":
		return b.startUnit(name, in.Service)
	case "boot.stop":
		return b.stopUnit(name, in.Service)
	}
	return parent.Result{}, errInitDenied
}

func listed(list []string) func(string) bool {
	return func(s string) bool { return s != "" && slices.Contains(list, s) }
}

func oneRow(cols []string, r parent.Row) (parent.Result, error) {
	return parent.Result{Kind: parent.KindRows, Cols: cols, Rows: []parent.Row{r}}, nil
}

// moduleBase is a modules.dep path's module name: the base name with at
// most one compression suffix removed, then .ko required and removed, -
// read as _. "" is no module.
func moduleBase(rel string) string {
	base := path.Base(rel)
	for _, sfx := range []string{".xz", ".zst", ".gz"} {
		if strings.HasSuffix(base, sfx) {
			base = strings.TrimSuffix(base, sfx)
			break
		}
	}
	if !strings.HasSuffix(base, ".ko") {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSuffix(base, ".ko"), "-", "_")
}

// depLine finds NAME's one line in modules.dep and its dependencies.
func (b *Boot) depLine(name string) ([]string, error) {
	var found []string
	n := 0
	for _, line := range strings.Split(b.ModulesDep, "\n") {
		if line == "" {
			continue
		}
		file, deps, ok := strings.Cut(line, ":")
		if !ok {
			return nil, code("module_tree")
		}
		paths := append([]string{file}, strings.Fields(deps)...)
		for _, p := range paths {
			if strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.Contains(p, "..") {
				return nil, code("module_tree")
			}
		}
		if moduleBase(file) != name {
			continue
		}
		n++
		found = nil
		for _, d := range paths[1:] {
			found = append(found, moduleBase(d))
		}
	}
	if n != 1 {
		return nil, code("module_unknown")
	}
	return found, nil
}

func (b *Boot) loadModule(name string) (parent.Result, error) {
	if b.ModulesDisabled {
		return parent.Result{}, code("module_disabled")
	}
	if !b.SigEnforce && !b.Lockdown {
		if !b.UnsignedDev {
			return parent.Result{}, code("module_sig_unenforced")
		}
		b.Logs = append(b.Logs, "module_unsigned_dev "+name)
	}
	deps, err := b.depLine(name)
	if err != nil {
		return parent.Result{}, err
	}
	// modules.dep lists every dependency, nearest first: load them from
	// the far end so each finds its own already live.
	for i := len(deps) - 1; i >= 0; i-- {
		if deps[i] == "" {
			return parent.Result{}, code("module_tree")
		}
		if err := b.loadOne(deps[i]); err != nil {
			return parent.Result{}, err
		}
	}
	state := "already"
	if !b.Loaded[name] {
		if err := b.loadOne(name); err != nil {
			return parent.Result{}, err
		}
		state = "done"
	}
	return oneRow([]string{"name", "state", "deps"}, parent.Row{"name": name, "state": state, "deps": strings.Join(deps, ",")})
}

// loadOne is one finit_module: as the kernel, it refuses a module any of
// whose own dependencies is not live yet (an unknown symbol).
func (b *Boot) loadOne(m string) error {
	if b.Loaded[m] {
		return nil
	}
	deps, err := b.depLine(m)
	if err != nil {
		return err
	}
	for _, d := range deps {
		if !b.Loaded[d] {
			return code("module_failed")
		}
	}
	if b.LoadFail[m] {
		return code("module_failed")
	}
	b.Loaded[m] = true
	b.LoadOrder = append(b.LoadOrder, m)
	return nil
}

// checkDevice is device wait's identity check, once: the node, its kind
// and the declared sysfs attributes.
func (b *Boot) checkDevice(name string) (*FakeDev, error) {
	d, ok := b.Host.Devices[name]
	if !ok {
		return nil, code("not_found")
	}
	if !b.DevTmpfs {
		return nil, code("device_unsafe")
	}
	node := b.Devs[d.Path]
	if node == nil {
		return nil, nil
	}
	if node.Symlink || node.Kind != d.Kind {
		return nil, code("device_unsafe")
	}
	for a, want := range d.Match {
		if strings.TrimSpace(node.Attrs[a]) != strings.TrimSpace(want) {
			return nil, code("device_mismatch")
		}
	}
	return node, nil
}

func (b *Boot) waitDevice(name string) (parent.Result, error) {
	node, err := b.checkDevice(name)
	if err != nil {
		return parent.Result{}, err
	}
	if node == nil {
		// The double does not sleep: an absent node has run out of time.
		return parent.Result{}, code("timeout")
	}
	dev := fmt.Sprintf("%d:%d", node.Major, node.Minor)
	b.waited[name] = dev
	return oneRow([]string{"name", "path", "major", "minor"}, parent.Row{"name": name, "path": b.Host.Devices[name].Path,
		"major": strconv.Itoa(node.Major), "minor": strconv.Itoa(node.Minor)})
}

func (b *Boot) ensureMount(name string, g grant.Grant) (parent.Result, error) {
	m, ok := b.Host.Mounts[name]
	if !ok {
		return parent.Result{}, code("not_found")
	}
	if b.Dirs[m.Target] != "root 0755" && b.Dirs[m.Target] != "root 0700" && b.Dirs[m.Target] != "root 0750" {
		return parent.Result{}, code("mount_target")
	}
	for _, root := range g.FileWrite {
		if within(m.Target, root) || within(root, m.Target) {
			return parent.Result{}, code("mount_overlap")
		}
	}
	dev := ""
	if m.Source != "tmpfs" {
		node, err := b.checkDevice(m.Source)
		if err != nil {
			return parent.Result{}, err
		}
		if node == nil {
			return parent.Result{}, code("device_absent")
		}
		dev = fmt.Sprintf("%d:%d", node.Major, node.Minor)
		// Only through the device device wait checked in this boot.
		if checked, ok := b.waited[m.Source]; !ok || checked != dev {
			return parent.Result{}, code("device_changed")
		}
	}
	ro := slices.Contains(m.Options, "ro")
	if cur := b.HostMounts[m.Target]; cur != nil {
		if cur.FSType == m.FSType && cur.Dev == dev && cur.RO == ro {
			return oneRow([]string{"name", "target", "fstype", "state"}, parent.Row{"name": name, "target": m.Target, "fstype": m.FSType, "state": "already"})
		}
		return parent.Result{}, code("mount_conflict")
	}
	opts := append([]string(nil), m.Options...)
	if !slices.Contains(opts, "suid") {
		opts = append(opts, "nosuid")
	}
	if !slices.Contains(opts, "dev") {
		opts = append(opts, "nodev")
	}
	b.HostMounts[m.Target] = &HostMount{FSType: m.FSType, Dev: dev, RO: ro, Opts: opts}
	return oneRow([]string{"name", "target", "fstype", "state"}, parent.Row{"name": name, "target": m.Target, "fstype": m.FSType, "state": "done"})
}

func (b *Boot) applyNetwork(name, self string) (parent.Result, error) {
	n, ok := b.Host.Network[name]
	if !ok {
		return parent.Result{}, code("not_found")
	}
	cols := []string{"name", "link", "state", "changes"}
	if n.Kind == "unit" {
		// The profile is the unit's grant: units_start need not list it.
		res, err := b.startUnit(n.Unit, self)
		if err != nil {
			return res, err
		}
		return oneRow(cols, parent.Row{"name": name, "link": "", "state": res.Rows[0]["state"], "changes": "0"})
	}
	for _, u := range n.Requires {
		// Ready, or a oneshot that exited 0: running is not ready.
		if unit := b.Units[u]; unit == nil || !(unit.State == "ready" || (unit.State == "exited" && unit.Exit == 0)) {
			return parent.Result{}, code("net_unguarded")
		}
	}
	var link *FakeLink
	for _, l := range b.Links {
		if l.MAC == n.Link {
			if link != nil {
				return parent.Result{}, code("net_unknown_link")
			}
			link = l
		}
	}
	if link == nil || (n.Name != "" && link.Name != n.Name) {
		return parent.Result{}, code("net_unknown_link")
	}
	changes := 0
	if n.MTU != 0 && link.MTU != n.MTU {
		link.MTU = n.MTU
		changes++
	}
	if !link.Up {
		link.Up = true
		changes++
	}
	for _, a := range n.Addresses {
		if !slices.Contains(link.Addrs, a) {
			link.Addrs = append(link.Addrs, a)
			changes++
		}
	}
	for _, r := range n.Routes {
		if !slices.Contains(link.Routes, r) {
			link.Routes = append(link.Routes, r)
			changes++
		}
	}
	state := "done"
	if changes == 0 {
		state = "already"
	}
	return oneRow(cols, parent.Row{"name": name, "link": link.Name, "state": state, "changes": strconv.Itoa(changes)})
}

// reaches is whether from needs or comes after to, directly or through
// other units.
func (b *Boot) reaches(from, to string, seen map[string]bool) bool {
	if seen[from] {
		return false
	}
	seen[from] = true
	u := b.Units[from]
	if u == nil {
		return false
	}
	for _, d := range append(append([]string(nil), u.Needs...), u.After...) {
		if d == to || b.reaches(d, to, seen) {
			return true
		}
	}
	return false
}

var unitCols = []string{"name", "state", "since", "pid"}

// startUnit is service start, and a unit network profile.
func (b *Boot) startUnit(name, self string) (parent.Result, error) {
	u := b.Units[name]
	switch {
	case u == nil:
		return parent.Result{}, code("not_found")
	case name == self:
		return parent.Result{}, code("boot_self")
	case u.BootOps:
		return parent.Result{}, code("boot_chain")
	case u.Boot:
		return parent.Result{}, code("boot_unit_auto")
	case b.reaches(name, self, map[string]bool{}):
		return parent.Result{}, code("boot_cycle")
	case b.touched[name]:
		return parent.Result{}, code("boot_once")
	}
	b.touched[name] = true
	state := "done"
	if u.State == "running" || u.State == "exited" || u.State == "ready" {
		state = "already"
	} else {
		u.State = "running"
	}
	return oneRow(append([]string(nil), unitCols...), parent.Row{"name": name, "state": state, "since": "2026-11-24T00:00:00Z", "pid": "0"})
}

func (b *Boot) stopUnit(name, self string) (parent.Result, error) {
	u := b.Units[name]
	switch {
	case u == nil:
		return parent.Result{}, code("not_found")
	case name == self:
		return parent.Result{}, code("boot_self")
	case u.BootOps:
		return parent.Result{}, code("boot_chain")
	case u.Essential:
		return parent.Result{}, code("boot_essential")
	case b.touched[name]:
		return parent.Result{}, code("boot_once")
	}
	b.touched[name] = true
	state := "done"
	if u.State == "stopped" {
		state = "already"
	}
	u.State = "stopped"
	return oneRow(append([]string(nil), unitCols...), parent.Row{"name": name, "state": state, "since": "2026-11-24T00:00:00Z", "pid": "0"})
}

// BootCalls is how many boot verbs passed the gates in this boot.
func (b *Boot) BootCalls() int { return b.calls }
