package lang

import (
	"strconv"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto/prototest"
)

func bootOpsGrant() grant.Grant {
	return grant.Grant{
		V: "sash/3", Actor: "boot:hostprep", Host: "web1", Purpose: grant.PurposeBoot, LeaseID: "boot-hostprep-1",
		Entitlements: []string{"boot.module", "boot.device", "boot.mount", "boot.net", "boot.start", "boot.stop", "svc.status", "file.write"},
		FileWrite:    []string{"/run/hostprep"},
		Service:      "hostprep", User: "hostprep", Group: "hostprep",
		Modules:     []string{"virtio_blk", "virtio_net", "e1000", "broken", "ext4", "twice"},
		Devices:     []string{"data", "logs"},
		HostMounts:  []string{"data", "scratch"},
		NetProfiles: []string{"lan", "wan", "dhcp"},
		UnitsStart:  []string{"dhcp", "firewall", "hostprep", "otherprep", "app", "late", "nosuch"},
		UnitsStop:   []string{"getty-tty1", "sshd", "hostprep", "otherprep", "dhcp", "nosuch"},
		Serial:      1,
	}
}

const hostprepService = `service {
  name hostprep
  user hostprep
  group hostprep
  restart never
}
`

func newBootInit(t *testing.T) *prototest.Init {
	t.Helper()
	in := prototest.NewInit("hostprep", bootOpsGrant())
	in.Boot = prototest.NewBoot("hostprep")
	return in
}

func runBoot(src, entry string, in *prototest.Init) Outcome {
	return Run(src, bootOpsGrant(), in, Options{Entry: entry})
}

func startFn(body string) string { return hostprepService + "fn start() {\n" + body + "\n}\n" }

// The phase 4 script, against the double: every verb goes out as Key and
// Argv only, device wait's timeout as whole seconds, and the host ends
// prepared.
func TestBootScriptRunsThroughTheInit(t *testing.T) {
	in := newBootInit(t)
	src := startFn(`module load virtio_blk
device wait data timeout 30s
mount ensure data
module load virtio_net
network apply lan
service start dhcp`)
	out := runBoot(src, "start", in)
	if out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	b := in.Boot
	for _, m := range []string{"virtio_blk", "virtio_net", "virtio", "virtio_ring", "failover", "net_failover"} {
		if !b.Loaded[m] {
			t.Errorf("module %s not loaded: %v", m, b.Loaded)
		}
	}
	hm := b.HostMounts["/data"]
	if hm == nil || hm.Dev != "253:16" || !strings.Contains(strings.Join(hm.Opts, ","), "nosuid") || !strings.Contains(strings.Join(hm.Opts, ","), "nodev") {
		t.Fatalf("mount %+v", hm)
	}
	if l := b.Links[0]; !l.Up || strings.Join(l.Addrs, ",") != "10.0.2.15/24" || len(l.Routes) != 1 {
		t.Fatalf("link %+v", l)
	}
	if b.Units["dhcp"].State != "running" {
		t.Fatal("dhcp not started")
	}
	calls := in.Calls()
	if len(calls) != 6 {
		t.Fatalf("calls %v", calls)
	}
	for _, c := range calls {
		if c.Path != "" || len(c.Body) != 0 || c.Mode != "" || !strings.HasPrefix(c.Key, "boot.") {
			t.Errorf("verb carries more than Key and Argv: %+v", c)
		}
	}
	if strings.Join(calls[1].Argv, " ") != "device wait data timeout 30" {
		t.Fatalf("device wait argv %q", calls[1].Argv)
	}
	if !strings.Contains(out.Stdout, "done") {
		t.Fatalf("rows: %q", out.Stdout)
	}
}

// Every verb is idempotent: a second run (a new session, the same boot)
// finds the host already prepared.
func TestBootVerbsAreIdempotent(t *testing.T) {
	in := newBootInit(t)
	src := startFn("module load virtio_blk\ndevice wait data timeout 5s\nmount ensure data\nnetwork apply lan")
	if out := runBoot(src, "start", in); out.Status != 0 || strings.Contains(out.Stdout, "already") {
		t.Fatalf("first: %+v", out)
	}
	out := runBoot(src, "start", in)
	if out.Status != 0 || strings.Contains(out.Stdout, "done") || strings.Count(out.Stdout, "already") != 3 {
		t.Fatalf("second: %+v", out)
	}
}

// Shapes are usage errors, exit 2, before the init: no path, alias, file,
// dash, flag or second name ever reaches it.
func TestBootShapes(t *testing.T) {
	for line, want := range map[string]string{
		"module load ../x":                       "usage module load NAME",
		"module load a/b":                        "usage module load NAME",
		"module load x.ko":                       "usage module load NAME",
		"module load virtio-net":                 "usage module load NAME",
		"module load fs-ext4":                    "usage module load NAME",
		"module load pci:v00001af4":              "usage module load NAME",
		"module load " + strings.Repeat("a", 56): "usage module load NAME",
		"module load":                            "usage module load NAME",
		"module load virtio_blk ext4":            "usage module load NAME",
		"module load -f virtio_blk":              "usage module load NAME",
		"module unload virtio_blk":               "usage module load NAME",
		"module virtio_blk":                      "usage module load NAME",
		"mount ensure /mnt/x":                    "usage mount ensure NAME",
		"mount ensure":                           "usage mount ensure NAME",
		"mount ensure data type tmpfs":           "usage mount ensure NAME",
		"mount ensure -o data":                   "usage mount ensure NAME",
		"device wait a/b timeout 5s":             "usage device wait NAME timeout D",
		"device wait data":                       "usage device wait NAME timeout D",
		"device wait data 5s":                    "usage device wait NAME timeout D",
		"device wait data timeout 61s":           msgBootWait,
		"device wait data timeout 0s":            msgBootWait,
		"device wait data timeout 500ms":         msgBootWait,
		"device wait data timeout 1.5s":          msgBootWait,
		"device wait data timeout 2m":            msgBootWait,
		"device wait data timeout 30":            msgBootWait,
		"network apply":                          "usage network apply NAME",
		"network apply Lan":                      "usage network apply NAME",
		"network up lan":                         "usage network apply NAME",
		"service start Dhcp":                     "usage service start NAME",
		"service start a_b":                      "usage service start NAME",
		"service stop -x":                        "usage service stop NAME",
		"service stop a b":                       "usage service stop NAME",
	} {
		in := newBootInit(t)
		out := runBoot(startFn(line), "start", in)
		if out.Status != exitType || !strings.Contains(out.Stderr, want) {
			t.Errorf("%q: %d %q, want %q", line, out.Status, out.Stderr, want)
		}
		if len(in.Calls()) != 0 {
			t.Errorf("%q: the init was called", line)
		}
	}
	// 60s is the most a wait takes; 1m is the same.
	in := newBootInit(t)
	if out := runBoot(startFn("device wait data timeout 1m"), "start", in); out.Status != 0 || strings.Join(in.Calls()[0].Argv, " ") != "device wait data timeout 60" {
		t.Fatalf("1m: %+v %v", out, in.Calls())
	}
	// mount and service keep their own forms.
	if _, prob := classify([]string{"mount", "tmpfs", "/mnt/app"}, nil); prob != nil {
		t.Fatalf("mount: %v", prob)
	}
	if info, prob := classify([]string{"service", "status", "dhcp"}, nil); prob != nil || info.key != "svc.status" {
		t.Fatalf("service status: %v", prob)
	}
}

// A secret is never a name.
func TestBootSecretRefused(t *testing.T) {
	for _, body := range []string{"module load $m", "service start $m", "mount ensure $m", "device wait $m timeout 5s"} {
		in := newBootInit(t)
		out := runBoot(startFn("declare -s m = virtio_blk\n"+body), "start", in)
		if out.Status != exitType || !strings.Contains(out.Stderr, "secret_refused") || len(in.Calls()) != 0 {
			t.Errorf("%q: %+v %v", body, out, in.Calls())
		}
	}
}

// Only a sash/3 purpose boot grant carries a boot key: a sash/1 or sash/2
// grant listing it is denied, and so is a sash/3 grant without the key.
func TestBootKeysNeedASash3Grant(t *testing.T) {
	v1 := bootOpsGrant()
	v1.V, v1.Purpose, v1.Actor = "sash/1", grant.PurposeOpen, "ryan"
	f := &fake{}
	if out := Run("module load virtio_blk", v1, f, Options{}); out.Status != exitDenied || out.Stderr != "denied boot.module\n" || len(f.calls) != 0 {
		t.Fatalf("sash/1: %+v %v", out, f.calls)
	}
	v2 := bootGrant("app")
	v2.Entitlements = append(v2.Entitlements, "boot.module", "boot.start")
	v2.Modules, v2.UnitsStart = []string{"virtio_blk"}, []string{"dhcp"}
	in := prototest.NewInit("app", v2)
	in.Boot = prototest.NewBoot("app")
	for _, line := range []string{"module load virtio_blk", "service start dhcp"} {
		out := Run(appService+"fn start() {\n"+line+"\n}\n", v2, in, Options{Entry: "start"})
		if out.Status != exitType || !strings.Contains(out.Stderr, "denied boot.") || len(in.Calls()) != 0 {
			t.Errorf("sash/2 %q: %+v %v", line, out, in.Calls())
		}
	}
	// The service verbs keep working under sash/3.
	in = newBootInit(t)
	in.Status["dhcp"] = "running"
	if out := runBoot(startFn("service status dhcp"), "start", in); out.Status != 0 {
		t.Fatalf("svc.status under sash/3: %+v", out)
	}
	for key, line := range map[string]string{
		"boot.module": "module load virtio_blk", "boot.device": "device wait data timeout 5s", "boot.mount": "mount ensure data",
		"boot.net": "network apply lan", "boot.start": "service start dhcp", "boot.stop": "service stop getty-tty1",
	} {
		g := bootOpsGrant()
		g.Entitlements = []string{"svc.status"}
		in := prototest.NewInit("hostprep", g)
		in.Boot = prototest.NewBoot("hostprep")
		out := Run(startFn(line), g, in, Options{Entry: "start"})
		// --entry exits 2 for a denial (init contract item 12).
		if out.Status != exitType || out.Stderr != "denied "+key+"\n" || len(in.Calls()) != 0 {
			t.Errorf("%s without the key: %+v", key, out)
		}
	}
	// A boot grant runs only an entry.
	if out := Run(startFn("module load virtio_blk"), bootOpsGrant(), &fake{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "entry_missing") {
		t.Fatalf("no entry: %+v", out)
	}
}

// A name the grant does not list is denied before the init.
func TestBootNamesNotInTheGrant(t *testing.T) {
	for line, want := range map[string]string{
		"module load e1001":            "denied module",
		"device wait disk2 timeout 5s": "denied device",
		"mount ensure home":            "denied host mount",
		"network apply mgmt":           "denied network profile",
		"service start getty-tty1":     "denied service start",
		"service stop firewall":        "denied service stop",
	} {
		in := newBootInit(t)
		out := runBoot(startFn(line), "start", in)
		// --entry exits 2 for a denial (init contract item 12).
		if out.Status != exitType || out.Stderr != want+"\n" || len(in.Calls()) != 0 {
			t.Errorf("%q: %+v %v", line, out, in.Calls())
		}
	}
}

// Where a boot verb may run: only fn start, never in await, check, par or
// atomic.
func TestBootPlacement(t *testing.T) {
	for _, c := range []struct{ name, src, entry, want string }{
		{"stop", hostprepService + "fn start() { echo x }\nfn stop() {\nservice stop getty-tty1\n}\n", "stop", "boot_entry"},
		{"reload", hostprepService + "fn reload() {\nmodule load virtio_blk\n}\n", "reload", "boot_entry"},
		{"check", hostprepService + "fn check() {\ndevice wait data timeout 5s\n}\n", "check", "boot_entry"},
		{"stop through a helper", hostprepService + "fn h() { module load virtio_blk }\nfn stop() {\nh\n}\n", "stop", "boot_entry"},
		{"await", startFn("await timeout 10s every 1s { device wait data timeout 5s }"), "start", "boot_in_await"},
		{"await mutating", startFn("await timeout 10s every 1s { module load virtio_blk }"), "start", "boot_in_await"},
		{"par", startFn("par 2 {\nmodule load virtio_blk\nmodule load virtio_net\n}"), "start", "boot_in_par"},
		{"atomic", startFn("atomic { module load virtio_blk }"), "start", "atomic_irreversible: boot.module"},
		{"atomic through a helper", hostprepService + "fn h() { service stop getty-tty1 }\nfn start() {\natomic { h }\n}\n", "start", "atomic_irreversible: boot.stop"},
	} {
		in := newBootInit(t)
		in.Boot.Entry = c.entry
		out := runBoot(c.src, c.entry, in)
		if out.Status != exitType || !strings.Contains(out.Stderr, c.want) {
			t.Errorf("%s: %d %q, want %q", c.name, out.Status, out.Stderr, c.want)
		}
		if len(in.Calls()) != 0 {
			t.Errorf("%s: the init was called: %v", c.name, in.Calls())
		}
	}
	// device wait is a read: atomic does not refuse it statically (the
	// double serves no atomic.*, so the block is unavailable instead).
	in := newBootInit(t)
	out := runBoot(startFn("atomic { device wait data timeout 5s }"), "start", in)
	if strings.Contains(out.Stderr, "atomic_irreversible") {
		t.Fatalf("device wait in atomic: %+v", out)
	}
}

// 64 boot verbs per run: the 65th is boot_call_limit before the init, and
// set cannot raise it. The init counts per boot, so a new session gets no
// fresh budget.
func TestBootCallLimit(t *testing.T) {
	in := newBootInit(t)
	out := runBoot(startFn(strings.Repeat("module load virtio_blk\n", 65)), "start", in)
	if out.Status != exitType || !strings.Contains(out.Stderr, "boot_call_limit") || len(in.Calls()) != 64 {
		t.Fatalf("65th: %d %q %d", out.Status, out.Stderr, len(in.Calls()))
	}
	if out := runBoot(startFn("set bootmax 1000\nmodule load virtio_blk"), "start", newBootInit(t)); out.Status == 0 {
		t.Fatalf("set bootmax: %+v", out)
	}
	// The init's own count: 40, then 40 more in a new session.
	in = newBootInit(t)
	if out := runBoot(startFn(strings.Repeat("module load virtio_blk\n", 40)), "start", in); out.Status != 0 {
		t.Fatalf("first 40: %+v", out)
	}
	out = runBoot(startFn(strings.Repeat("module load virtio_blk\n", 40)), "start", in)
	if out.Status != exitType || !strings.Contains(out.Stderr, "refused boot.module: boot_call_limit (") || in.Boot.BootCalls() != 64 {
		t.Fatalf("second 40: %+v %d", out, in.Boot.BootCalls())
	}
	// Boot verbs count against verbmax too.
	out = runBoot(startFn("set verbmax 2\nmodule load virtio_blk\nmodule load virtio_blk\nmodule load virtio_blk"), "start", newBootInit(t))
	if out.Status == 0 {
		t.Fatalf("verbmax: %+v", out)
	}
}

// Plan mode prints a line for every boot verb, device wait included, and
// calls nothing.
func TestBootPlanLines(t *testing.T) {
	in := newBootInit(t)
	st := NewState()
	st.LockPlan()
	src := startFn("module load virtio_blk\ndevice wait data timeout 30s\nmount ensure data\nnetwork apply lan\nservice start dhcp\nservice stop getty-tty1")
	out := Run(src, bootOpsGrant(), in, Options{Entry: "start", State: st})
	want := "plan: would load module virtio_blk\nplan: would wait for device data (up to 30s)\nplan: would ensure mount data\n" +
		"plan: would apply network lan\nplan: would start service dhcp\nplan: would stop service getty-tty1\n"
	if out.Status != 0 || out.Stderr != want || out.Stdout != "" {
		t.Fatalf("%+v", out)
	}
	if len(in.Calls()) != 0 {
		t.Fatalf("plan called the init: %v", in.Calls())
	}
}

// The init's refusals print as refused boot.KEY: CODE (why), exit 2. Each
// is the double re-checking what sash cannot see.
func TestBootInitRefusals(t *testing.T) {
	for _, c := range []struct {
		name, line, code string
		setup            func(*prototest.Boot)
	}{
		{"signatures off", "module load virtio_blk", "module_sig_unenforced", func(b *prototest.Boot) { b.SigEnforce = false }},
		{"modules disabled", "module load virtio_blk", "module_disabled", func(b *prototest.Boot) { b.ModulesDisabled = true }},
		{"not in modules.dep", "module load e1000", "module_unknown", nil},
		{"kernel refused", "module load broken", "module_failed", func(b *prototest.Boot) {
			b.ModulesDep += "kernel/drivers/x/broken.ko:\n"
			b.LoadFail["broken"] = true
		}},
		{"dep with ..", "module load ext4", "module_tree", func(b *prototest.Boot) { b.ModulesDep += "kernel/fs/../../../tmp/evil.ko:\n" }},
		{"dep absolute", "module load ext4", "module_tree", func(b *prototest.Boot) { b.ModulesDep += "/tmp/evil.ko:\n" }},
		{"wrong disk", "device wait data timeout 5s", "device_mismatch", func(b *prototest.Boot) { b.Devs["/dev/vdb"].Attrs["serial"] = "OTHER" }},
		{"symlinked node", "device wait data timeout 5s", "device_unsafe", func(b *prototest.Boot) { b.Devs["/dev/vdb"].Symlink = true }},
		{"not devtmpfs", "device wait data timeout 5s", "device_unsafe", func(b *prototest.Boot) { b.DevTmpfs = false }},
		{"char not block", "device wait data timeout 5s", "device_unsafe", func(b *prototest.Boot) { b.Devs["/dev/vdb"].Kind = "char" }},
		{"absent", "device wait logs timeout 5s", "timeout", nil},
		{"mount without a wait", "mount ensure data", "device_changed", nil},
		{"device gone", "mount ensure data", "device_absent", func(b *prototest.Boot) { delete(b.Devs, "/dev/vdb") }},
		{"target not root's", "mount ensure scratch", "mount_target", func(b *prototest.Boot) { b.Dirs["/scratch"] = "app 0777" }},
		{"target taken", "mount ensure scratch", "mount_conflict", func(b *prototest.Boot) { b.HostMounts["/scratch"] = &prototest.HostMount{FSType: "ext4", Dev: "8:1"} }},
		{"unguarded", "network apply wan", "net_unguarded", nil},
		{"no such link", "network apply lan", "net_unknown_link", func(b *prototest.Boot) { b.Links = nil }},
		{"two links", "network apply lan", "net_unknown_link", func(b *prototest.Boot) {
			b.Links = append(b.Links, &prototest.FakeLink{MAC: "52:54:00:12:34:56", Name: "eth1"})
		}},
		{"self", "service start hostprep", "boot_self", nil},
		{"chain", "service start otherprep", "boot_chain", nil},
		{"boot unit", "service start app", "boot_unit_auto", nil},
		{"cycle", "service start late", "boot_cycle", nil},
		{"twice", "service start dhcp\nservice start dhcp", "boot_once", nil},
		{"start then stop", "service start dhcp\nservice stop dhcp", "boot_once", nil},
		{"essential", "service stop sshd", "boot_essential", nil},
		{"stop self", "service stop hostprep", "boot_self", nil},
		{"unknown unit", "service start nosuch", "not_found", nil},
		{"after boot", "module load virtio_blk", "boot_over", func(b *prototest.Boot) { b.Booted = true }},
		{"other host", "module load virtio_blk", "sash_grant_host", func(b *prototest.Boot) { b.HostID = "web2" }},
		{"no trust file", "module load virtio_blk", "sash_grant_host", func(b *prototest.Boot) { b.HostID = "" }},
		{"not opted in", "module load virtio_blk", "sash_boot_ops", func(b *prototest.Boot) { b.OptedIn = false }},
		{"init's entry", "module load virtio_blk", "boot_entry", func(b *prototest.Boot) { b.Entry = "stop" }},
	} {
		in := newBootInit(t)
		if c.setup != nil {
			c.setup(in.Boot)
		}
		out := runBoot(startFn(c.line), "start", in)
		key := in.Calls()[len(in.Calls())-1].Key
		want := "refused " + key + ": " + c.code + " ("
		if out.Status != exitType || !strings.Contains(out.Stderr, want) {
			t.Errorf("%s: %d %q, want %q", c.name, out.Status, out.Stderr, want)
		}
	}
	// The disk under the node changes between device wait and mount
	// ensure (two sessions, one boot).
	in := newBootInit(t)
	if out := runBoot(startFn("device wait data timeout 5s"), "start", in); out.Status != 0 {
		t.Fatalf("wait: %+v", out)
	}
	in.Boot.Devs["/dev/vdb"].Minor = 32
	if out := runBoot(startFn("mount ensure data"), "start", in); out.Status != exitType || !strings.Contains(out.Stderr, "refused boot.mount: device_changed (") {
		t.Fatalf("swapped: %+v", out)
	}
	// A unit profile starts its unit without units_start listing it.
	in = newBootInit(t)
	g := bootOpsGrant()
	g.UnitsStart = []string{}
	out := Run(startFn("network apply dhcp"), g, in, Options{Entry: "start"})
	if out.Status != 0 || in.Boot.Units["dhcp"].State != "running" {
		t.Fatalf("unit profile: %+v", out)
	}
	// The development override: signatures off, logged.
	in = newBootInit(t)
	in.Boot.SigEnforce, in.Boot.UnsignedDev = false, true
	if out := runBoot(startFn("module load virtio_blk"), "start", in); out.Status != 0 || strings.Join(in.Boot.Logs, ";") != "module_unsigned_dev virtio_blk" {
		t.Fatalf("unsigned dev: %+v %v", out, in.Boot.Logs)
	}
	// A firewall unit guards a public profile: ready, or a oneshot that
	// exited 0. Running is not ready, nor a oneshot that failed.
	for state, want := range map[string]int{"ready": 0, "exited": 0, "running": exitType, "exited 1": exitType, "stopped": exitType} {
		in = newBootInit(t)
		in.Boot.Links = append(in.Boot.Links, &prototest.FakeLink{MAC: "52:54:00:aa:bb:cc", Name: "eth1"})
		fw := in.Boot.Units["firewall"]
		fw.State = state
		if state == "exited 1" {
			fw.State, fw.Exit = "exited", 1
		}
		out := runBoot(startFn("network apply wan"), "start", in)
		if out.Status != want || (want != 0 && !strings.Contains(out.Stderr, "refused boot.net: net_unguarded (")) {
			t.Errorf("wan with the firewall %s: %+v", state, out)
		}
	}
	// A started firewall is running, not yet ready.
	in = newBootInit(t)
	in.Boot.Links = append(in.Boot.Links, &prototest.FakeLink{MAC: "52:54:00:aa:bb:cc", Name: "eth1"})
	if out := runBoot(startFn("service start firewall\nnetwork apply wan"), "start", in); out.Status != exitType || !strings.Contains(out.Stderr, "net_unguarded") {
		t.Fatalf("wan right after starting the firewall: %+v", out)
	}
}

// Modules load dependencies first, each only once its own are live, in
// the kernel's order: the sequence is pinned exactly.
func TestBootModuleLoadOrder(t *testing.T) {
	in := newBootInit(t)
	if out := runBoot(startFn("module load virtio_net\nmodule load virtio_blk"), "start", in); out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	if got := strings.Join(in.Boot.LoadOrder, " "); got != "virtio virtio_ring failover net_failover virtio_net virtio_blk" {
		t.Fatalf("load order %q", got)
	}
	// A modules.dep line that names a dependency after the module that
	// needs it cannot load: the kernel would refuse the unknown symbol.
	in = newBootInit(t)
	good := "virtio_net.ko.zst: kernel/drivers/net/net_failover.ko kernel/net/core/failover.ko "
	bad := "virtio_net.ko.zst: kernel/net/core/failover.ko kernel/drivers/net/net_failover.ko "
	if !strings.Contains(in.Boot.ModulesDep, good) {
		t.Fatal("fixture changed")
	}
	in.Boot.ModulesDep = strings.Replace(in.Boot.ModulesDep, good, bad, 1)
	if out := runBoot(startFn("module load virtio_net"), "start", in); out.Status != exitType || !strings.Contains(out.Stderr, "refused boot.module: module_failed (") {
		t.Fatalf("misordered line: %+v %v", out, in.Boot.LoadOrder)
	}
	if in.Boot.Loaded["net_failover"] || in.Boot.Loaded["virtio_net"] {
		t.Fatalf("loaded past the missing dependency: %v", in.Boot.LoadOrder)
	}
	// One compression suffix at most, then .ko: twice.ko.gz.xz is no module.
	in = newBootInit(t)
	in.Boot.ModulesDep += "kernel/x/twice.ko.gz.xz:\n"
	if out := runBoot(startFn("module load twice"), "start", in); out.Status != exitType || !strings.Contains(out.Stderr, "module_unknown") {
		t.Fatalf("two suffixes: %+v", out)
	}
	in = newBootInit(t)
	in.Boot.ModulesDep += "kernel/x/twice.ko.xz:\n"
	if out := runBoot(startFn("module load twice"), "start", in); out.Status != 0 {
		t.Fatalf("one suffix: %+v", out)
	}
}

// The init re-checks what sash already checked: a verb sent around sash
// is refused the same.
func TestInitRechecksBootVerbs(t *testing.T) {
	ok := parent.Verb{Key: "boot.module", Argv: []string{"module", "load", "virtio_blk"}}
	for name, v := range map[string]parent.Verb{
		"not listed":                 {Key: "boot.module", Argv: []string{"module", "load", "e1001"}},
		"path name":                  {Key: "boot.module", Argv: []string{"module", "load", "../x"}},
		"dash name":                  {Key: "boot.module", Argv: []string{"module", "load", "virtio-net"}},
		"extra word":                 {Key: "boot.module", Argv: []string{"module", "load", "virtio_blk", "debug=1"}},
		"wrong verb":                 {Key: "boot.module", Argv: []string{"device", "wait", "virtio_blk"}},
		"path":                       {Key: "boot.module", Argv: ok.Argv, Path: "/lib/modules/x.ko"},
		"body":                       {Key: "boot.module", Argv: ok.Argv, Body: []byte("x")},
		"mode":                       {Key: "boot.module", Argv: ok.Argv, Mode: "0644"},
		"long wait":                  {Key: "boot.device", Argv: []string{"device", "wait", "data", "timeout", "61"}},
		"wait unit":                  {Key: "boot.device", Argv: []string{"device", "wait", "data", "timeout", "30s"}},
		"mount target":               {Key: "boot.mount", Argv: []string{"mount", "ensure", "/mnt/x"}},
		"start listed only for stop": {Key: "boot.start", Argv: []string{"service", "start", "getty-tty1"}},
		"stop listed only for start": {Key: "boot.stop", Argv: []string{"service", "stop", "firewall"}},
	} {
		in := newBootInit(t)
		if _, err := in.Do(v); err == nil || err.Error() != "denied" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A sash/2 grant listing the key and the name.
	v2 := bootGrant("app")
	v2.Entitlements = append(v2.Entitlements, "boot.module")
	v2.Modules = []string{"virtio_blk"}
	in := prototest.NewInit("app", v2)
	in.Boot = prototest.NewBoot("app")
	if _, err := in.Do(ok); err == nil || err.Error() != "denied" {
		t.Fatalf("sash/2: %v", err)
	}
	// The grant's own gates at the init, each alone: no key, a name
	// outside the grammar listed in an unverified grant, an empty host
	// with no trust file.
	nokey := bootOpsGrant()
	nokey.Entitlements = []string{"boot.device"}
	in = prototest.NewInit("hostprep", nokey)
	in.Boot = prototest.NewBoot("hostprep")
	if _, err := in.Do(ok); err == nil || err.Error() != "denied" {
		t.Fatalf("no key: %v", err)
	}
	bad := bootOpsGrant()
	bad.Modules = append(bad.Modules, "../x")
	in = prototest.NewInit("hostprep", bad)
	in.Boot = prototest.NewBoot("hostprep")
	if _, err := in.Do(parent.Verb{Key: "boot.module", Argv: []string{"module", "load", "../x"}}); err == nil || err.Error() != "denied" {
		t.Fatalf("listed path: %v", err)
	}
	nohost := bootOpsGrant()
	nohost.Host = ""
	in = prototest.NewInit("hostprep", nohost)
	in.Boot = prototest.NewBoot("hostprep")
	in.Boot.HostID = ""
	if _, err := in.Do(ok); err == nil || err.Error() != "sash_grant_host" {
		t.Fatalf("no trust file, empty host: %v", err)
	}
	// A modules.dep name with .. in it is refused even when clean.
	in = newBootInit(t)
	in.Boot.ModulesDep += "kernel/x/..evil.ko:\n"
	if _, err := in.Do(ok); err == nil || err.Error() != "module_tree" {
		t.Fatalf("dotdot name: %v", err)
	}
	// An unknown boot key, even with no argv, is denied and charges no
	// budget.
	unk := bootOpsGrant()
	unk.Entitlements = append(unk.Entitlements, "boot.fsck")
	in = prototest.NewInit("hostprep", unk)
	in.Boot = prototest.NewBoot("hostprep")
	if _, err := in.Do(parent.Verb{Key: "boot.fsck"}); err == nil || err.Error() != "denied" || in.Boot.BootCalls() != 0 {
		t.Fatalf("unknown key: %v, %d calls", err, in.Boot.BootCalls())
	}
	// No Boot configured: every boot verb is denied.
	in = prototest.NewInit("hostprep", bootOpsGrant())
	if _, err := in.Do(ok); err == nil {
		t.Fatal("no boot side")
	}
	in = newBootInit(t)
	if _, err := in.Do(ok); err != nil {
		t.Fatalf("valid: %v", err)
	}
}

func TestBootNeedsDescribeAndLint(t *testing.T) {
	src := startFn("module load virtio_blk\ndevice wait data timeout 30s\nmount ensure data\nnetwork apply lan\nservice start dhcp\nservice stop getty-tty1")
	out := Needs(src, NeedsOptions{})
	for _, want := range []string{"boot.module  virtio_blk  yes", "boot.device  data        no", "boot.mount   data        yes", "boot.net     lan         yes", "boot.start   dhcp        yes", "boot.stop    getty-tty1  yes"} {
		if !strings.Contains(out.Stdout, want) {
			t.Errorf("needs lacks %q:\n%s", want, out.Stdout)
		}
	}
	if out.Status != 0 {
		t.Fatalf("literal names incomplete: %+v", out)
	}
	pol := Needs(src, NeedsOptions{Policy: true})
	if !strings.Contains(pol.Stdout, "# boot.module virtio_blk: only a sash/3 boot grant carries this") || strings.Contains(pol.Stdout, "\n--") || strings.HasPrefix(pol.Stdout, "--") {
		t.Fatalf("policy:\n%s", pol.Stdout)
	}
	dyn := Needs(startFn("let m = virtio_blk\nmodule load $m"), NeedsOptions{})
	if dyn.Status != 1 || !strings.Contains(dyn.Stdout, "dynamic") {
		t.Fatalf("dynamic: %+v", dyn)
	}
	req := Describe(src+"fn stop() { echo bye }\n", DescribeOptions{GrantRequest: true})
	for _, want := range []string{`"v": "sash/3"`, `"purpose": "boot"`, `"actor": "boot:hostprep"`, `"modules": [
    "virtio_blk"
  ]`, `"devices": [
    "data"
  ]`, `"host_mounts": [
    "data"
  ]`, `"net_profiles": [
    "lan"
  ]`, `"units_start": [
    "dhcp"
  ]`, `"units_stop": [
    "getty-tty1"
  ]`, `"boot.module"`, `"boot.stop"`} {
		if !strings.Contains(req.Stdout, want) {
			t.Errorf("grant request lacks %q:\n%s", want, req.Stdout)
		}
	}
	if req.Status != 0 {
		t.Fatalf("request: %+v", req)
	}
	// A dynamic name is left out of the list, and the request is incomplete.
	req = Describe(startFn("let m = virtio_blk\nmodule load $m"), DescribeOptions{GrantRequest: true})
	if req.Status != 1 || !strings.Contains(req.Stdout, `"modules": []`) || !strings.Contains(req.Stdout, `"boot.module"`) {
		t.Fatalf("dynamic request: %+v", req)
	}
	// A subcommand from a value: service $s NAME may be start or stop,
	// mount $w NAME may be mount ensure. Each is a dynamic row, the
	// description is incomplete, the request is sash/3 with the keys, and
	// lint sees a possible boot verb.
	for _, c := range []struct{ body, key string }{
		{"let s = start\nservice $s dhcp", "boot.start"},
		{"let s = start\nservice $s dhcp", "boot.stop"},
		{"let w = ensure\nmount $w data", "boot.mount"},
	} {
		n := Needs(startFn(c.body), NeedsOptions{})
		if n.Status != 1 || !strings.Contains(n.Stdout, c.key) || !strings.Contains(n.Stdout, "dynamic") {
			t.Errorf("%q needs: %+v", c.body, n)
		}
		r := Describe(startFn(c.body), DescribeOptions{GrantRequest: true})
		if r.Status != 1 || !strings.Contains(r.Stdout, `"v": "sash/3"`) || !strings.Contains(r.Stdout, `"`+c.key+`"`) {
			t.Errorf("%q request: %+v", c.body, r)
		}
		l := Lint(hostprepService + "fn start() { echo x }\nfn stop() {\n" + c.body + "\n}\n")
		if !strings.Contains(l.Stdout, "boot-outside-start") {
			t.Errorf("%q lint:\n%s", c.body, l.Stdout)
		}
	}
	// Without a boot verb the request stays sash/2.
	if r := Describe(appService+"fn start() { proc main }\n", DescribeOptions{GrantRequest: true}); !strings.Contains(r.Stdout, `"v": "sash/2"`) || strings.Contains(r.Stdout, "modules") {
		t.Fatalf("sash/2 request: %s", r.Stdout)
	}
	lint := Lint(hostprepService + "fn h() { module load virtio_blk }\nfn start() { h }\nfn stop() {\n service stop getty-tty1\n}\nfn reload() {\n h\n}\nfn check() {\n device wait data timeout 5s\n}\n")
	if strings.Count(lint.Stdout, "boot-outside-start") != 3 || !strings.Contains(lint.Stdout, "boot-outside-start  error") || !strings.Contains(lint.Stdout, "boot.module through h") {
		t.Fatalf("lint:\n%s", lint.Stdout)
	}
	if clean := Lint(src); strings.Contains(clean.Stdout, "boot-outside-start") {
		t.Fatalf("clean:\n%s", clean.Stdout)
	}
	atomicLint := Lint(startFn("atomic { module load virtio_blk }"))
	if !strings.Contains(atomicLint.Stdout, "atomic cannot undo boot.module") {
		t.Fatalf("atomic lint:\n%s", atomicLint.Stdout)
	}
}

// Every builtin boot form names a key in bootField, and the table, the
// forms and the double agree on the keys.
func TestBootFormsTable(t *testing.T) {
	forms := map[string]bool{}
	for _, b := range Builtins() {
		if strings.HasPrefix(b.Key, "boot.") {
			forms[b.Form] = true
		}
	}
	for _, f := range bootForms {
		if !forms[f.verb+" "+f.sub] || bootField[f.key] == "" || !mutating[f.key] != (f.key == "boot.device") || irreversible[f.key] != mutating[f.key] {
			t.Errorf("%s %s: table %v field %q", f.verb, f.sub, forms[f.verb+" "+f.sub], bootField[f.key])
		}
		if !prototest.Handles(parent.Verb{Key: f.key}) {
			t.Errorf("%s: the double does not handle it", f.key)
		}
	}
	if len(forms) != 6 || len(bootForms) != 6 {
		t.Fatalf("forms %v", forms)
	}
	for i := 1; i <= 60; i++ {
		if s, ok := bootWaitSeconds(strconv.Itoa(i) + "s"); !ok || s != i {
			t.Fatalf("%ds", i)
		}
	}
}
