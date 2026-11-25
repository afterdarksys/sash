package grant

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

var updateVectors = flag.Bool("update", false, "rewrite testdata/sash-grant-v3-vectors.json")

const vectorsFile = "testdata/sash-grant-v3-vectors.json"

// vectorNow is the clock every vector is verified at.
const vectorNow = 1_700_000_000

func sampleV3() Grant {
	return Grant{
		V:            "sash/3",
		Actor:        "boot:hostprep",
		Host:         "web1",
		Purpose:      PurposeBoot,
		Exp:          0,
		LeaseID:      "boot-hostprep-1",
		Entitlements: []string{"boot.module", "boot.device", "boot.mount", "boot.net", "boot.start", "boot.stop"},
		FileRead:     []string{},
		FileWrite:    []string{"/run/hostprep"},
		Service:      "hostprep",
		Exec:         []string{},
		Caps:         []string{},
		Mounts:       []string{},
		MountSources: []string{},
		User:         "hostprep",
		Group:        "hostprep",
		EnvNames:     []string{},
		Modules:      []string{"virtio_blk", "virtio_net"},
		Devices:      []string{"data"},
		HostMounts:   []string{"data"},
		NetProfiles:  []string{"lan"},
		UnitsStart:   []string{"dhcp"},
		UnitsStop:    []string{"getty-tty1"},
		Serial:       3,
	}
}

// vectorKey is a fixed test key: the seed is 32 copies of b. Never a real
// key.
func vectorKey(b byte) pair {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
	return pair{pub: priv.Public().(ed25519.PublicKey), priv: priv}
}

type vectorCase struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`
	// Want is the error token, or "" when the document verifies.
	Want string `json:"want"`
	// Message is the exact signed bytes, for a valid case.
	Message string `json:"message,omitempty"`
}

type vectorFile struct {
	V            string       `json:"v"`
	Note         string       `json:"note"`
	FleetPub     string       `json:"fleet_pub"`
	EmergencyPub string       `json:"emergency_pub"`
	Now          int64        `json:"now"`
	Cases        []vectorCase `json:"cases"`
}

// buildVectors makes every case from fixed keys. Ed25519 is deterministic,
// so the same code always makes the same bytes: the file pins Message.
func buildVectors(t *testing.T) vectorFile {
	t.Helper()
	fleet, emergency, wrong := vectorKey(1), vectorKey(2), vectorKey(3)
	sign := func(g Grant, k pair) string { return string(mustSign(t, g, k.priv)) }
	edit := func(f func(*Grant)) string {
		g := sampleV3()
		f(&g)
		return sign(g, fleet)
	}
	// rawEdit signs the valid grant, then changes its JSON as a map.
	rawEdit := func(doc string, f func(map[string]any)) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	// sigOver puts on doc a fleet signature over msg instead of its own.
	sigOver := func(doc string, msg []byte) string {
		return string(replaceSig(t, []byte(doc), ed25519.Sign(fleet.priv, msg)))
	}
	valid := sign(sampleV3(), fleet)
	msg, err := Message(sampleV3())
	if err != nil {
		t.Fatal(err)
	}
	v3body, _ := json.Marshal(v3Fields(sampleV3()))
	v2 := sampleV2()
	v2doc := sign(v2, fleet)
	v2body, _ := json.Marshal(v2Fields(v2))
	withExp := sampleV3()
	withExp.Exp = 1_800_000_000
	expMsg, _ := Message(withExp)
	sixtyFive := make([]string, 65)
	for i := range sixtyFive {
		sixtyFive[i] = fmt.Sprintf("m%d", i)
	}

	cases := []vectorCase{
		{Name: "valid", Doc: valid, Message: string(msg)},
		{Name: "valid exp in the future", Doc: sign(withExp, fleet), Message: string(expMsg)},
		{Name: "tampered module name", Doc: strings.Replace(valid, `"virtio_blk"`, `"virtio_bkk"`, 1), Want: "grant_bad_sig"},
		{Name: "tampered serial", Doc: strings.Replace(valid, `"serial":3`, `"serial":4`, 1), Want: "grant_bad_sig"},
		{Name: "tampered signature", Doc: string(flipSig(t, []byte(valid))), Want: "grant_bad_sig"},
		{Name: "wrong key", Doc: sign(sampleV3(), wrong), Want: "grant_bad_sig"},
		{Name: "emergency key", Doc: sign(sampleV3(), emergency), Want: "grant_bad_sig"},
		{Name: "expired", Doc: edit(func(g *Grant) { g.Exp = vectorNow - 1 }), Want: "grant_expired"},
		{Name: "oversized", Doc: rawEdit(valid, func(m map[string]any) { m["lease_id"] = strings.Repeat("x", MaxDocument) }), Want: "grant_bad_shape"},
		{Name: "v3 body signed under the v2 domain", Doc: sigOver(valid, append([]byte(domainV2), v3body...)), Want: "grant_bad_sig"},
		{Name: "v3 body signed under the v1 domain", Doc: sigOver(valid, append([]byte(domain), v3body...)), Want: "grant_bad_sig"},
		{Name: "v2 body signed under the v3 domain", Doc: sigOver(v2doc, append([]byte(domainV3), v2body...)), Want: "grant_bad_sig"},
		{Name: "v2 signature on a v3 document", Doc: sigOver(valid, append([]byte(domainV2), v2body...)), Want: "grant_bad_sig"},
		{Name: "v2 document relabelled sash/3", Doc: rawEdit(v2doc, func(m map[string]any) { m["v"] = "sash/3" }), Want: "grant_bad_shape"},
		{Name: "v2 document with a v3 field", Doc: rawEdit(v2doc, func(m map[string]any) { m["modules"] = []string{"virtio_blk"} }), Want: "grant_bad_shape"},
		{Name: "v2 document with purpose boot", Doc: func() string { g := sampleV2(); g.Purpose = PurposeBoot; return sign(g, fleet) }(), Want: "grant_bad_shape"},
		{Name: "v3 with purpose service", Doc: edit(func(g *Grant) { g.Purpose = PurposeService }), Want: "grant_bad_shape"},
		{Name: "v3 with actor service:", Doc: edit(func(g *Grant) { g.Actor = "service:hostprep" }), Want: "grant_bad_shape"},
		{Name: "missing modules", Doc: rawEdit(valid, func(m map[string]any) { delete(m, "modules") }), Want: "grant_bad_shape"},
		{Name: "missing units_stop", Doc: rawEdit(valid, func(m map[string]any) { delete(m, "units_stop") }), Want: "grant_bad_shape"},
		{Name: "missing file_read", Doc: rawEdit(valid, func(m map[string]any) { delete(m, "file_read") }), Want: "grant_bad_shape"},
		{Name: "missing allow_private", Doc: rawEdit(valid, func(m map[string]any) { delete(m, "allow_private") }), Want: "grant_bad_shape"},
		{Name: "missing exp", Doc: rawEdit(valid, func(m map[string]any) { delete(m, "exp") }), Want: "grant_bad_shape"},
		{Name: "null devices", Doc: rawEdit(valid, func(m map[string]any) { m["devices"] = nil }), Want: "grant_bad_shape"},
		{Name: "unknown field", Doc: rawEdit(valid, func(m map[string]any) { m["params"] = []string{"x=1"} }), Want: "grant_bad_shape"},
		{Name: "trailing data", Doc: valid + " {}", Want: "grant_bad_shape"},
		{Name: "ensure in mount_sources", Doc: edit(func(g *Grant) { g.MountSources = []string{"ensure"} }), Want: "grant_bad_shape"},
		{Name: "duplicate module", Doc: edit(func(g *Grant) { g.Modules = []string{"virtio_blk", "virtio_blk"} }), Want: "grant_bad_shape"},
		{Name: "duplicate unit", Doc: edit(func(g *Grant) { g.UnitsStart = []string{"dhcp", "dhcp"} }), Want: "grant_bad_shape"},
		{Name: "65 modules", Doc: edit(func(g *Grant) { g.Modules = sixtyFive }), Want: "grant_bad_shape"},
		{Name: "65 devices", Doc: edit(func(g *Grant) { g.Devices = sixtyFive }), Want: "grant_bad_shape"},
		{Name: "module with a dash", Doc: edit(func(g *Grant) { g.Modules = []string{"virtio-net"} }), Want: "grant_bad_shape"},
		{Name: "module path", Doc: edit(func(g *Grant) { g.Modules = []string{"../x"} }), Want: "grant_bad_shape"},
		{Name: "module file", Doc: edit(func(g *Grant) { g.Modules = []string{"x.ko"} }), Want: "grant_bad_shape"},
		{Name: "module alias", Doc: edit(func(g *Grant) { g.Modules = []string{"pci:v00001af4"} }), Want: "grant_bad_shape"},
		{Name: "module 56 bytes", Doc: edit(func(g *Grant) { g.Modules = []string{strings.Repeat("a", 56)} }), Want: "grant_bad_shape"},
		{Name: "device path", Doc: edit(func(g *Grant) { g.Devices = []string{"a/b"} }), Want: "grant_bad_shape"},
		{Name: "host mount path", Doc: edit(func(g *Grant) { g.HostMounts = []string{"/mnt/x"} }), Want: "grant_bad_shape"},
		{Name: "profile upper case", Doc: edit(func(g *Grant) { g.NetProfiles = []string{"Lan"} }), Want: "grant_bad_shape"},
		{Name: "unit with an underscore", Doc: edit(func(g *Grant) { g.UnitsStop = []string{"getty_tty1"} }), Want: "grant_bad_shape"},
		{Name: "unit flag", Doc: edit(func(g *Grant) { g.UnitsStart = []string{"-dhcp"} }), Want: "grant_bad_shape"},
		{Name: "star", Doc: edit(func(g *Grant) { g.Entitlements = []string{"boot.module", "*"} }), Want: "grant_star"},
		{Name: "serial 0", Doc: edit(func(g *Grant) { g.Serial = 0 }), Want: "grant_bad_shape"},
		{Name: "file root slash", Doc: edit(func(g *Grant) { g.FileWrite = []string{"/"} }), Want: "grant_root_slash"},
	}
	return vectorFile{
		V:            "sash-grant-v3-vectors/1",
		Note:         "Test keys only: fleet is ed25519 seed 32*0x01, emergency 32*0x02, the wrong key 32*0x03. Verify each doc at now with fleet_pub and emergency_pub; want is the error token, empty when it verifies. message is the exact signed bytes of a valid case. Regenerate with go test ./internal/grant -run TestV3Vectors -update.",
		FleetPub:     base64.StdEncoding.EncodeToString(fleet.pub),
		EmergencyPub: base64.StdEncoding.EncodeToString(emergency.pub),
		Now:          vectorNow,
		Cases:        cases,
	}
}

// The golden vectors: the file is what the code makes (so Message and Sign
// are pinned byte for byte), and every case verifies to its token. sahinit
// copies the file and runs the same cases.
func TestV3Vectors(t *testing.T) {
	built := buildVectors(t)
	out, err := json.MarshalIndent(built, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	if *updateVectors {
		if err := os.WriteFile(vectorsFile, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	disk, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk, out) {
		t.Fatalf("%s differs from what the code signs; run with -update only when the change is intended", vectorsFile)
	}
	var f vectorFile
	if err := json.Unmarshal(disk, &f); err != nil {
		t.Fatal(err)
	}
	fleet, _ := base64.StdEncoding.DecodeString(f.FleetPub)
	emergency, _ := base64.StdEncoding.DecodeString(f.EmergencyPub)
	for _, c := range f.Cases {
		g, err := Verify([]byte(c.Doc), fleet, emergency, time.Unix(f.Now, 0))
		if token(err) != c.Want {
			t.Errorf("%s: %v, want %q", c.Name, err, c.Want)
			continue
		}
		if c.Want == "" {
			msg, err := Message(g)
			if err != nil || string(msg) != c.Message {
				t.Errorf("%s: message %q", c.Name, msg)
			}
			if !g.BootOps() || !g.Boot() {
				t.Errorf("%s: not a boot grant", c.Name)
			}
		}
	}
}

// The signed bytes: the v3 domain, then the sash/1 fields, the sash/2
// fields but serial, the six lists, and serial last.
func TestV3MessageOrder(t *testing.T) {
	msg, err := Message(sampleV3())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(msg, []byte("sash-grant-v3\n")) {
		t.Fatalf("domain %q", msg)
	}
	body := string(msg[len("sash-grant-v3\n"):])
	last := -1
	for _, k := range []string{`"v"`, `"actor"`, `"host"`, `"purpose"`, `"exp"`, `"lease_id"`, `"entitlements"`, `"file_read"`, `"file_write"`, `"allow_private"`,
		`"service"`, `"exec"`, `"caps"`, `"mounts"`, `"mount_sources"`, `"user"`, `"group"`, `"env_names"`,
		`"modules"`, `"devices"`, `"host_mounts"`, `"net_profiles"`, `"units_start"`, `"units_stop"`, `"serial"`} {
		i := strings.Index(body, k+":")
		if i <= last {
			t.Fatalf("%s out of order in %s", k, body)
		}
		last = i
	}
	// An absent list is written [], never null.
	g := sampleV3()
	g.Modules, g.UnitsStop = nil, nil
	msg, _ = Message(g)
	if !bytes.Contains(msg, []byte(`"modules":[]`)) || !bytes.Contains(msg, []byte(`"units_stop":[]`)) {
		t.Fatalf("absent list: %s", msg)
	}
	// sash/2's message is untouched: no boot list in it.
	v2, _ := Message(sampleV2())
	if bytes.Contains(v2, []byte(`"modules"`)) || !bytes.HasPrefix(v2, []byte("sash-grant-v2\n")) {
		t.Fatalf("sash/2 message changed: %s", v2)
	}
}

// The live signature swaps, besides the fixed vectors: a v2 grant's own
// signature moved onto a v3 document with the same sash/2 fields, and the
// reverse.
func TestV2V3SignatureSwapRefused(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(vectorNow, 0)
	v3 := sampleV3()
	v2 := sampleV2()
	v2.Service, v2.Actor, v2.User, v2.Group = v3.Service, "service:"+v3.Service, v3.User, v3.Group
	v2doc := mustSign(t, v2, fleet.priv)
	v3doc := mustSign(t, v3, fleet.priv)
	var m2, m3 map[string]any
	_ = json.Unmarshal(v2doc, &m2)
	_ = json.Unmarshal(v3doc, &m3)
	if _, err := Verify(replaceSigText(t, v3doc, m2["sig"].(string)), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("v2 sig on v3: %v", err)
	}
	if _, err := Verify(replaceSigText(t, v2doc, m3["sig"].(string)), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("v3 sig on v2: %v", err)
	}
	if _, err := Verify(v3doc, fleet.pub, emergency.pub, now); err != nil {
		t.Fatalf("valid v3: %v", err)
	}
}

func replaceSigText(t *testing.T, doc []byte, sig string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatal(err)
	}
	return replaceSig(t, doc, raw)
}

func TestBootOpsAndLists(t *testing.T) {
	g := sampleV3()
	if !g.BootOps() || !g.Boot() {
		t.Fatal("sash/3 boot")
	}
	if v2 := sampleV2(); v2.BootOps() || !v2.Boot() {
		t.Fatal("sash/2 service is Boot, never BootOps")
	}
	if (Grant{V: "sash/3", Purpose: PurposeService}).BootOps() || (Grant{V: "sash/2", Purpose: PurposeBoot}).BootOps() || (Grant{V: "sash/1", Purpose: PurposeBoot}).Boot() {
		t.Fatal("a mislabelled grant is a boot grant")
	}
	if !g.AllowsModule("virtio_blk") || g.AllowsModule("virtio_bl") || g.AllowsModule("") || !g.AllowsDevice("data") || !g.AllowsHostMount("data") ||
		!g.AllowsNetProfile("lan") || !g.AllowsUnitStart("dhcp") || g.AllowsUnitStart("getty-tty1") || !g.AllowsUnitStop("getty-tty1") || g.AllowsUnitStop("dhcp") {
		t.Fatal("lists")
	}
	// The same lists on a grant that is not sash/3 boot allow nothing.
	g.V = "sash/2"
	if g.AllowsModule("virtio_blk") || g.AllowsDevice("data") || g.AllowsUnitStart("dhcp") {
		t.Fatal("a list outside sash/3 allowed a name")
	}
	if err := CheckService(sampleV3(), "hostprep"); err != nil {
		t.Fatal(err)
	}
	if token(CheckService(sampleV3(), "app")) != "grant_service_mismatch" {
		t.Fatal("other service")
	}
}

// exp 0 is allowed for service and boot only.
func TestExpZeroForBoot(t *testing.T) {
	fleet, emergency := testKeys(t)
	if _, err := Verify(mustSign(t, sampleV3(), fleet.priv), fleet.pub, emergency.pub, time.Unix(4_000_000_000, 0)); err != nil {
		t.Fatalf("exp 0 boot: %v", err)
	}
}

// ensure is refused as a mount source in sash/2 too: mount ensure is
// always the boot verb, so such a source could never be mounted.
func TestEnsureMountSourceRefusedInV2(t *testing.T) {
	fleet, emergency := testKeys(t)
	g := sampleV2()
	g.MountSources = []string{"tmpfs", "ensure"}
	if _, err := Verify(mustSign(t, g, fleet.priv), fleet.pub, emergency.pub, time.Now()); token(err) != "grant_bad_shape" {
		t.Fatalf("ensure in sash/2: %v", err)
	}
}

func TestRequestVersion(t *testing.T) {
	g := Grant{Service: "app", Actor: "service:app", Entitlements: []string{"svc.spawn"}, User: "app", Group: "app"}
	b, err := Request(g)
	if err != nil || !bytes.Contains(b, []byte(`"v": "sash/2"`)) || !bytes.Contains(b, []byte(`"purpose": "service"`)) || bytes.Contains(b, []byte(`"modules"`)) {
		t.Fatalf("sash/2 request: %s %v", b, err)
	}
	g.Entitlements = append(g.Entitlements, "boot.module")
	g.Modules = []string{"virtio_blk"}
	b, err = Request(g)
	if err != nil || !bytes.Contains(b, []byte(`"v": "sash/3"`)) || !bytes.Contains(b, []byte(`"purpose": "boot"`)) || !bytes.Contains(b, []byte(`"actor": "boot:app"`)) {
		t.Fatalf("sash/3 request: %s %v", b, err)
	}
	if i, j := bytes.Index(b, []byte(`"units_stop"`)), bytes.Index(b, []byte(`"serial"`)); i < 0 || j < i {
		t.Fatalf("order: %s", b)
	}
}
