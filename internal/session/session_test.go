package session

import (
	"bytes"
	"crypto/ed25519"
	"io"
	"net"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto"
)

func TestOpenRunsSignedPipeline(t *testing.T) {
	fleet, emergency := keypair(t)
	doc, err := grant.Sign(grant.Grant{
		V:            "sash/1",
		Actor:        "ryan",
		Host:         "miniapps1",
		Purpose:      grant.PurposeOpen,
		Exp:          1_800_000_000,
		LeaseID:      "lease-1",
		Entitlements: []string{"ps"},
		FileRead:     []string{"/var/log"},
		FileWrite:    []string{},
	}, fleet.priv)
	if err != nil {
		t.Fatal(err)
	}
	agent, sashVerb := net.Pipe()
	defer agent.Close()
	defer sashVerb.Close()
	calls := 0
	go func() {
		_ = servePS(agent, &calls)
	}()
	g, p, err := Open(bytes.NewReader(doc), bytes.NewReader(fleet.pub), bytes.NewReader(emergency.pub), sashVerb, time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := lang.Run(`ps -ef | egrep sshd | head -n 1`, g, p, lang.Options{})
	if got.Status != 0 || got.Stdout != "root  1  /usr/sbin/sshd\n" {
		t.Fatalf("status %d stdout %q stderr %q", got.Status, got.Stdout, got.Stderr)
	}
	denied := lang.Run(`unit restart cron`, g, p, lang.Options{})
	if denied.Status != 126 || denied.Stderr != "denied unit.restart\n" || calls != 1 {
		t.Fatalf("denied status %d stderr %q calls %d", denied.Status, denied.Stderr, calls)
	}
}

func TestOpenRejectsBadGrants(t *testing.T) {
	fleet, emergency := keypair(t)
	now := time.Unix(1_700_000_000, 0)
	verbR, verbW := net.Pipe()
	defer verbR.Close()
	defer verbW.Close()
	_, _, err := Open(bytes.NewReader(nil), bytes.NewReader(fleet.pub), bytes.NewReader(emergency.pub), verbW, now)
	if err == nil || err.Error() != "grant_missing" {
		t.Fatalf("missing %v", err)
	}
	star, err := grant.Sign(grant.Grant{
		V: "sash/1", Actor: "ryan", Host: "miniapps1", Purpose: grant.PurposeOpen,
		Exp: 1_800_000_000, LeaseID: "lease-1", Entitlements: []string{"*"},
	}, fleet.priv)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Open(bytes.NewReader(star), bytes.NewReader(fleet.pub), bytes.NewReader(emergency.pub), verbW, now)
	if err == nil || err.Error() != "grant_star" {
		t.Fatalf("star %v", err)
	}
	old, err := grant.Sign(grant.Grant{
		V: "sash/1", Actor: "ryan", Host: "miniapps1", Purpose: grant.PurposeOpen,
		Exp: 1_600_000_000, LeaseID: "lease-1", Entitlements: []string{"ps"},
	}, fleet.priv)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Open(bytes.NewReader(old), bytes.NewReader(fleet.pub), bytes.NewReader(emergency.pub), verbW, now)
	if err == nil || err.Error() != "grant_expired" {
		t.Fatalf("expired %v", err)
	}
	good, err := grant.Sign(grant.Grant{
		V: "sash/1", Actor: "ryan", Host: "miniapps1", Purpose: grant.PurposeEmergency,
		Exp: 1_800_000_000, LeaseID: "lease-1", Entitlements: []string{"ps"},
	}, fleet.priv)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Open(bytes.NewReader(good), bytes.NewReader(fleet.pub), bytes.NewReader(emergency.pub), verbW, now)
	if err == nil || err.Error() != "grant_bad_sig" {
		t.Fatalf("wrong key %v", err)
	}
	_, _, err = Open(bytes.NewReader(good), bytes.NewReader(fleet.pub), bytes.NewReader(fleet.pub), verbW, now)
	if err == nil || err.Error() != "grant_bad_sig" {
		t.Fatalf("same key %v", err)
	}
	big := bytes.Repeat([]byte("x"), grant.MaxDocument+1)
	_, _, err = Open(bytes.NewReader(big), bytes.NewReader(fleet.pub), bytes.NewReader(emergency.pub), verbW, now)
	if err == nil || err.Error() != "grant_bad_shape" {
		t.Fatalf("oversize %v", err)
	}
	openDoc, err := grant.Sign(grant.Grant{
		V: "sash/1", Actor: "ryan", Host: "miniapps1", Purpose: grant.PurposeOpen,
		Exp: 1_800_000_000, LeaseID: "lease-1", Entitlements: []string{"ps"},
	}, fleet.priv)
	if err != nil {
		t.Fatal(err)
	}
	short := fleet.pub[:ed25519.PublicKeySize-1]
	_, _, err = Open(bytes.NewReader(openDoc), bytes.NewReader(short), bytes.NewReader(emergency.pub), verbW, now)
	if err == nil || err.Error() != "grant_bad_sig" {
		t.Fatalf("short key %v", err)
	}
	// An extra byte must not be trimmed back to a key that would verify.
	long := append(append([]byte{}, fleet.pub...), 0)
	_, _, err = Open(bytes.NewReader(openDoc), bytes.NewReader(long), bytes.NewReader(emergency.pub), verbW, now)
	if err == nil || err.Error() != "grant_bad_sig" {
		t.Fatalf("long key %v", err)
	}
}

type pair struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func keypair(t *testing.T) (fleet, emergency pair) {
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

func servePS(rw io.ReadWriter, calls *int) error {
	return proto.Serve(rw, func(v parent.Verb) (parent.Result, error) {
		if v.Key != "ps" {
			return parent.Result{}, io.ErrUnexpectedEOF
		}
		*calls++
		return parent.Result{
			Kind: parent.KindRows,
			Cols: []string{"user", "pid", "cmd"},
			Rows: []parent.Row{
				{"user": "root", "pid": "1", "cmd": "/usr/sbin/sshd"},
				{"user": "root", "pid": "2", "cmd": "cron"},
			},
		}, nil
	})
}
