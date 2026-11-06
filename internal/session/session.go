// Package session opens the pipes a parent hands sash.
//
// Threats: an empty, oversized, or malformed grant, a short or swapped
// public key, identical purpose keys, a fleet signature on an emergency
// grant, a "*", and an expired exp. Open fails closed and returns the grant
// error token. It does not open a pubkey path, hold a private key, or run a
// verb. The agent re-checks the grant before a helper runs. A read error is
// a token, not a prompt: the underlying error would hide the contract token.
package session

import (
	"crypto/ed25519"
	"io"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
	"github.com/afterdarksys/sash/internal/proto"
)

// Open reads one grant and two raw public keys, verifies the grant, and
// returns the verb client. now is the caller's clock. A zero-byte grant is
// grant_missing. More than grant.MaxDocument bytes is grant_bad_shape. Each
// key must be exactly 32 bytes or the result is grant_bad_sig.
func Open(grantR, fleetR, emergencyR io.Reader, verb io.ReadWriter, now time.Time) (grant.Grant, parent.Parent, error) {
	doc, err := readCap(grantR, grant.MaxDocument+1)
	if err != nil {
		return grant.Grant{}, nil, grant.ErrBadShape
	}
	if len(doc) == 0 {
		return grant.Grant{}, nil, grant.ErrMissing
	}
	if len(doc) > grant.MaxDocument {
		return grant.Grant{}, nil, grant.ErrBadShape
	}
	fleet, err := readKey(fleetR)
	if err != nil {
		return grant.Grant{}, nil, err
	}
	emergency, err := readKey(emergencyR)
	if err != nil {
		return grant.Grant{}, nil, err
	}
	g, err := grant.Verify(doc, fleet, emergency, now)
	if err != nil {
		return grant.Grant{}, nil, err
	}
	return g, &proto.Client{RW: verb}, nil
}

func readCap(r io.Reader, n int) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, int64(n)))
}

func readKey(r io.Reader) (ed25519.PublicKey, error) {
	b, err := readCap(r, ed25519.PublicKeySize+1)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, grant.ErrBadSig
	}
	return ed25519.PublicKey(b), nil
}
