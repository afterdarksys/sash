package grant

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleV2() Grant {
	return Grant{
		V:            "sash/2",
		Actor:        "service:nginx",
		Host:         "web1",
		Purpose:      PurposeService,
		Exp:          0,
		LeaseID:      "boot-nginx-1",
		Entitlements: []string{"svc.spawn", "svc.signal", "file.write"},
		FileRead:     []string{"/etc/nginx"},
		FileWrite:    []string{"/run/nginx"},
		Service:      "nginx",
		Exec:         []string{"/usr/sbin/nginx"},
		Caps:         []string{"CAP_NET_BIND_SERVICE"},
		Mounts:       []string{"/run/nginx/tmp"},
		MountSources: []string{"tmpfs", "/srv/nginx/static"},
		User:         "www",
		Group:        "www",
		Serial:       7,
	}
}

func TestV2VerifiesWithTheFleetKeyAndNeverExpires(t *testing.T) {
	fleet, emergency := testKeys(t)
	doc := mustSign(t, sampleV2(), fleet.priv)
	// exp 0 is never: far in the future it still verifies.
	g, err := Verify(doc, fleet.pub, emergency.pub, time.Unix(4_000_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if g.Service != "nginx" || g.Serial != 7 || !g.AllowsExec("/usr/sbin/nginx") || g.AllowsExec("/usr/sbin") || !g.AllowsCap("CAP_NET_BIND_SERVICE") || !g.AllowsMount("/run/nginx/tmp") || g.AllowsMount("/run/nginx") {
		t.Fatalf("grant %+v", g)
	}
	if _, err := Verify(mustSign(t, sampleV2(), emergency.priv), fleet.pub, emergency.pub, time.Now()); token(err) != "grant_bad_sig" {
		t.Fatalf("emergency key on a boot grant: %v", err)
	}
	// A boot grant with a real exp expires like any other.
	exp := sampleV2()
	exp.Exp = 1_700_000_000
	if _, err := Verify(mustSign(t, exp, fleet.priv), fleet.pub, emergency.pub, time.Unix(1_700_000_001, 0)); token(err) != "grant_expired" {
		t.Fatalf("expired boot grant: %v", err)
	}
}

// The signed bytes: the v2 domain, then the sash/1 fields, then service,
// exec, caps, mounts, serial in that order.
func TestV2MessageOrder(t *testing.T) {
	msg, err := Message(sampleV2())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(msg, []byte("sash-grant-v2\n")) {
		t.Fatalf("domain %q", msg)
	}
	body := string(msg[len("sash-grant-v2\n"):])
	last := -1
	for _, k := range []string{`"v"`, `"actor"`, `"host"`, `"purpose"`, `"exp"`, `"lease_id"`, `"entitlements"`, `"file_read"`, `"file_write"`, `"allow_private"`, `"service"`, `"exec"`, `"caps"`, `"mounts"`, `"mount_sources"`, `"user"`, `"group"`, `"serial"`} {
		i := strings.Index(body, k+":")
		if i <= last {
			t.Fatalf("%s out of order in %s", k, body)
		}
		last = i
	}
	v1, err := Message(sample(PurposeOpen))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(v1, []byte(`"service"`)) || !bytes.HasPrefix(v1, []byte("sash-grant-v1\n")) {
		t.Fatalf("sash/1 message changed: %s", v1)
	}
}

// A v1 signature does not verify as v2, nor the reverse: the same key, the
// signature moved between documents.
func TestV1V2SignatureSwapRefused(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	v1 := mustSign(t, sample(PurposeOpen), fleet.priv)
	v2 := mustSign(t, sampleV2(), fleet.priv)
	sigOf := func(doc []byte) string {
		var m map[string]any
		if err := json.Unmarshal(doc, &m); err != nil {
			t.Fatal(err)
		}
		return m["sig"].(string)
	}
	swap := func(doc []byte, sig string) []byte {
		var m map[string]any
		if err := json.Unmarshal(doc, &m); err != nil {
			t.Fatal(err)
		}
		m["sig"] = sig
		out, _ := json.Marshal(m)
		return out
	}
	if _, err := Verify(swap(v2, sigOf(v1)), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("v1 sig on v2: %v", err)
	}
	if _, err := Verify(swap(v1, sigOf(v2)), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("v2 sig on v1: %v", err)
	}
	// The same v2 fields signed under the v1 domain do not verify.
	g := sampleV2()
	body, _ := json.Marshal(v2Fields(g))
	if _, err := Verify(withSig(t, g, fleet.priv, append([]byte("sash-grant-v1\n"), body...)), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("v2 body under the v1 domain: %v", err)
	}
	// A v1 document carrying a v2 field is refused as an unknown field.
	var m map[string]any
	_ = json.Unmarshal(v1, &m)
	m["service"] = "nginx"
	doc, _ := json.Marshal(m)
	if _, err := Verify(doc, fleet.pub, emergency.pub, now); token(err) != "grant_bad_shape" {
		t.Fatalf("v1 with service: %v", err)
	}
}

func TestV2Shape(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	for name, c := range map[string]struct {
		edit func(*Grant)
		want string
	}{
		"purpose open":    {func(g *Grant) { g.Purpose = PurposeOpen }, "grant_bad_shape"},
		"bad name":        {func(g *Grant) { g.Service, g.Actor = "Nginx", "service:Nginx" }, "grant_bad_shape"},
		"actor":           {func(g *Grant) { g.Actor = "ryan" }, "grant_bad_shape"},
		"serial 0":        {func(g *Grant) { g.Serial = 0 }, "grant_bad_shape"},
		"relative exec":   {func(g *Grant) { g.Exec = []string{"nginx"} }, "grant_bad_shape"},
		"unclean exec":    {func(g *Grant) { g.Exec = []string{"/usr/sbin/../bin/sh"} }, "grant_bad_shape"},
		"slash mount":     {func(g *Grant) { g.Mounts = []string{"/"} }, "grant_root_slash"},
		"cap":             {func(g *Grant) { g.Caps = []string{"net_admin"} }, "grant_bad_shape"},
		"negative exp":    {func(g *Grant) { g.Exp = -1 }, "grant_bad_shape"},
		"star":            {func(g *Grant) { g.Entitlements = []string{"*"} }, "grant_star"},
		"file root slash": {func(g *Grant) { g.FileWrite = []string{"/"} }, "grant_root_slash"},
		"no user":         {func(g *Grant) { g.User = "" }, "grant_bad_shape"},
		"bad group":       {func(g *Grant) { g.Group = "Www" }, "grant_bad_shape"},
		"slash source":    {func(g *Grant) { g.MountSources = []string{"/"} }, "grant_root_slash"},
		"unclean source":  {func(g *Grant) { g.MountSources = []string{"/srv/../etc"} }, "grant_bad_shape"},
	} {
		g := sampleV2()
		c.edit(&g)
		if _, err := Verify(mustSign(t, g, fleet.priv), fleet.pub, emergency.pub, now); token(err) != c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	// A missing v2 field is a bad shape, not a zero value.
	var m map[string]any
	_ = json.Unmarshal(mustSign(t, sampleV2(), fleet.priv), &m)
	delete(m, "serial")
	doc, _ := json.Marshal(m)
	if _, err := Verify(doc, fleet.pub, emergency.pub, now); token(err) != "grant_bad_shape" {
		t.Fatalf("no serial: %v", err)
	}
}

// exp 0 is only for purpose service: any sash/1 purpose with it is
// grant_exp_zero, before the signature is looked at.
func TestExpZeroOnlyForService(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	for _, p := range []string{PurposeOpen, PurposeRoot, PurposeEmergency} {
		g := sample(p)
		g.Exp = 0
		key := fleet.priv
		if p == PurposeEmergency {
			key = emergency.priv
		}
		if _, err := Verify(mustSign(t, g, key), fleet.pub, emergency.pub, now); token(err) != "grant_exp_zero" {
			t.Errorf("%s exp 0: %v", p, err)
		}
	}
}

func TestCheckService(t *testing.T) {
	if err := CheckService(sampleV2(), "nginx"); err != nil {
		t.Fatal(err)
	}
	if token(CheckService(sampleV2(), "app")) != "grant_service_mismatch" {
		t.Fatal("other service accepted")
	}
	if token(CheckService(sample(PurposeOpen), "")) != "grant_service_mismatch" {
		t.Fatal("sash/1 grant accepted for a service")
	}
}

func TestIdentityAndMountSources(t *testing.T) {
	g := sampleV2()
	if CheckIdentity(g, "www", "www") != nil || CheckIdentity(g, "", "") != nil {
		t.Fatal("own identity refused")
	}
	if token(CheckIdentity(g, "root", "www")) != "grant_identity_mismatch" || token(CheckIdentity(g, "www", "wheel")) != "grant_identity_mismatch" {
		t.Fatal("other identity accepted")
	}
	for src, want := range map[string]bool{"tmpfs": true, "proc": false, "/srv/nginx/static": true, "/srv/nginx/static/img": true,
		"/srv/nginx": false, "/srv/nginx/static/../../../etc": false, "/": false} {
		if g.AllowsMountSource(src) != want {
			t.Errorf("%s: %v", src, !want)
		}
	}
	if (Grant{V: "sash/1", Purpose: PurposeOpen}).Boot() || !g.Boot() {
		t.Fatal("Boot")
	}
}
