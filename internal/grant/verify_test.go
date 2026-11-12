package grant

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVerifyAcceptsFleetGrant(t *testing.T) {
	fleet, emergency := testKeys(t)
	doc := mustSign(t, sample(PurposeOpen), fleet.priv)
	g, err := Verify(doc, fleet.pub, emergency.pub, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if g.Actor != "ryan" || !g.Has("ps") || g.Has("unit.restart") {
		t.Fatalf("grant %+v", g)
	}
}

func TestVerifyRejectsEmptyOversizedAndBadShape(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	if _, err := Verify(nil, fleet.pub, emergency.pub, now); token(err) != "grant_missing" {
		t.Fatalf("empty: %v", err)
	}
	big := bytes.Repeat([]byte("x"), MaxDocument+1)
	if _, err := Verify(big, fleet.pub, emergency.pub, now); token(err) != "grant_bad_shape" {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := Verify([]byte("{"), fleet.pub, emergency.pub, now); token(err) != "grant_bad_shape" {
		t.Fatalf("json: %v", err)
	}
	bad := sample(PurposeOpen)
	bad.V = "sash/2"
	if _, err := Verify(mustSign(t, bad, fleet.priv), fleet.pub, emergency.pub, now); token(err) != "grant_bad_shape" {
		t.Fatalf("version: %v", err)
	}
	rel := sample(PurposeOpen)
	rel.FileRead = []string{"var/log"}
	if _, err := Verify(mustSign(t, rel, fleet.priv), fleet.pub, emergency.pub, now); token(err) != "grant_bad_shape" {
		t.Fatalf("relative: %v", err)
	}
	escape := sample(PurposeOpen)
	escape.FileRead = []string{"/var/log/../../etc"}
	if _, err := Verify(mustSign(t, escape, fleet.priv), fleet.pub, emergency.pub, now); token(err) != "grant_bad_shape" {
		t.Fatalf("dotdot root: %v", err)
	}
}

func TestVerifyRejectsStarAfterSignature(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	star := sample(PurposeOpen)
	star.Entitlements = []string{"ps", "*"}
	if _, err := Verify(mustSign(t, star, fleet.priv), fleet.pub, emergency.pub, now); token(err) != "grant_star" {
		t.Fatalf("signed star: %v", err)
	}
	if _, err := Verify(flipSig(t, mustSign(t, star, fleet.priv)), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("tampered star: %v", err)
	}
}

func TestVerifyRejectsBadSigWrongKeyAndLease(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	doc := flipSig(t, mustSign(t, sample(PurposeOpen), fleet.priv))
	if _, err := Verify(doc, fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("flipped: %v", err)
	}
	if _, err := Verify(mustSign(t, sample(PurposeEmergency), fleet.priv), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("fleet key on emergency: %v", err)
	}
	if _, err := Verify(mustSign(t, sample(PurposeOpen), emergency.priv), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("emergency key on fleet grant: %v", err)
	}
	if _, err := Verify(mustSign(t, sample(PurposeRoot), fleet.priv), fleet.pub, emergency.pub, now); err != nil {
		t.Fatalf("terminal:root: %v", err)
	}
	same := fleet.pub
	if _, err := Verify(mustSign(t, sample(PurposeOpen), fleet.priv), same, same, now); token(err) != "grant_bad_sig" {
		t.Fatalf("identical pubkeys: %v", err)
	}
	leaseMsg := []byte("systemapi-lease-v1\nminiapps1\npty\n1700000000\n0123456789abcdef0123456789abcdef\n" + strings.Repeat("ab", 32) + "\n")
	if _, err := Verify(withSig(t, sample(PurposeOpen), fleet.priv, leaseMsg), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("lease envelope: %v", err)
	}
	actionMsg := []byte("pty\nsession\n1700000000\nstart")
	if _, err := Verify(withSig(t, sample(PurposeOpen), fleet.priv, actionMsg), fleet.pub, emergency.pub, now); token(err) != "grant_bad_sig" {
		t.Fatalf("action payload: %v", err)
	}
	msg, err := Message(sample(PurposeOpen))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(msg, []byte("sash-grant-v1\n")) || bytes.Contains(msg, []byte("systemapi-lease-v1")) {
		t.Fatalf("domain %q", msg)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	fleet, emergency := testKeys(t)
	g := sample(PurposeOpen)
	g.Exp = 1_700_000_000
	now := time.Unix(1_700_000_000, 0)
	if _, err := Verify(mustSign(t, g, fleet.priv), fleet.pub, emergency.pub, now); err != nil {
		t.Fatalf("exp == now is still valid: %v", err)
	}
	if _, err := Verify(mustSign(t, g, fleet.priv), fleet.pub, emergency.pub, now.Add(time.Second)); token(err) != "grant_expired" {
		t.Fatalf("past: %v", err)
	}
	if _, err := Verify(flipSig(t, mustSign(t, g, fleet.priv)), fleet.pub, emergency.pub, now.Add(time.Hour)); token(err) != "grant_bad_sig" {
		t.Fatalf("tampered expired: %v", err)
	}
}

type pair struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func testKeys(t *testing.T) (fleet, emergency pair) {
	t.Helper()
	var err error
	fleet.pub, fleet.priv, err = ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	emergency.pub, emergency.priv, err = ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return fleet, emergency
}

func sample(purpose string) Grant {
	return Grant{
		V:            "sash/1",
		Actor:        "ryan",
		Host:         "miniapps1",
		Purpose:      purpose,
		Exp:          1_800_000_000,
		LeaseID:      "lease-1",
		Entitlements: []string{"ps", "unit.status"},
		FileRead:     []string{"/var/log", "/etc"},
		FileWrite:    []string{"/var/tmp/ops"},
	}
}

func mustSign(t *testing.T, g Grant, priv ed25519.PrivateKey) []byte {
	t.Helper()
	doc, err := Sign(g, priv)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func withSig(t *testing.T, g Grant, priv ed25519.PrivateKey, msg []byte) []byte {
	t.Helper()
	doc := mustSign(t, g, priv)
	return replaceSig(t, doc, ed25519.Sign(priv, msg))
}

func token(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func flipSig(t *testing.T, doc []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(m["sig"].(string))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	return replaceSig(t, doc, raw)
}

func replaceSig(t *testing.T, doc []byte, sig []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	m["sig"] = base64.StdEncoding.EncodeToString(sig)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A root that is or cleans to "/" fails closed with grant_root_slash, in
// either list and next to a valid root, and the session does not open.
func TestVerifyRejectsSlashRoot(t *testing.T) {
	fleet, emergency := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	for _, root := range []string{"/", "//", "/./", "/..", "/../..", "///"} {
		for _, lists := range []func(*Grant){
			func(g *Grant) { g.FileRead = []string{root} },
			func(g *Grant) { g.FileWrite = []string{root} },
			func(g *Grant) { g.FileRead = []string{"/var/log", root} },
			func(g *Grant) { g.FileWrite = []string{root, "/tmp"} },
		} {
			g := sample(PurposeOpen)
			lists(&g)
			_, err := Verify(mustSign(t, g, fleet.priv), fleet.pub, emergency.pub, now)
			if token(err) != "grant_root_slash" {
				t.Errorf("%q in %+v: %v", root, g, err)
			}
			if err != nil && strings.Contains(err.Error(), root) && root != "/" {
				t.Errorf("error echoes the root: %v", err)
			}
		}
	}
	ok := sample(PurposeOpen)
	ok.FileRead, ok.FileWrite = []string{"/var/log"}, []string{"/tmp"}
	if _, err := Verify(mustSign(t, ok, fleet.priv), fleet.pub, emergency.pub, now); err != nil {
		t.Fatalf("valid roots: %v", err)
	}
	// under stays narrow too: a "/" root allows only "/" itself.
	g := Grant{FileRead: []string{"/"}}
	if g.AllowsRead("/etc/passwd") {
		t.Fatal("under widened")
	}
}
