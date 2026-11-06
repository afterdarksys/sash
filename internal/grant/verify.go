package grant

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"path"
	"strings"
)

// Threats: a forged or swapped sash/1 grant, a lease signature presented as a
// grant, a fleet signature on purpose emergency (and the reverse), a "*", an
// expired exp, a relative or unclean file root, identical purpose keys, and
// an oversized document. Verification fails closed before the prompt.
// Does NOT hold a private key. Does NOT treat systemapi-lease-v1 or
// "<action>\n<payload>" as a grant message. Does NOT open a pubkey path;
// the caller passes the raw 32-byte keys.

const (
	// MaxDocument is the largest grant sash will read.
	MaxDocument = 64 * 1024

	PurposeOpen      = "terminal:open"
	PurposeRoot      = "terminal:root"
	PurposeEmergency = "emergency"

	domain = "sash-grant-v1\n"
)

// Error is one stderr token from a grant the session will not start.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrMissing  Error = "grant_missing"
	ErrBadShape Error = "grant_bad_shape"
	ErrStar     Error = "grant_star"
	ErrBadSig   Error = "grant_bad_sig"
	ErrExpired  Error = "grant_expired"
)

type signed struct {
	V            string   `json:"v"`
	Actor        string   `json:"actor"`
	Host         string   `json:"host"`
	Purpose      string   `json:"purpose"`
	Exp          int64    `json:"exp"`
	LeaseID      string   `json:"lease_id"`
	Entitlements []string `json:"entitlements"`
	FileRead     []string `json:"file_read"`
	FileWrite    []string `json:"file_write"`
	AllowPrivate bool     `json:"allow_private"`
}

type wire struct {
	signed
	Sig string `json:"sig"`
}

// Message is the exact byte string an ed25519 key signs for this grant.
func Message(g Grant) ([]byte, error) {
	body, err := json.Marshal(signed{
		V:            g.V,
		Actor:        g.Actor,
		Host:         g.Host,
		Purpose:      g.Purpose,
		Exp:          g.Exp,
		LeaseID:      g.LeaseID,
		Entitlements: norm(g.Entitlements),
		FileRead:     norm(g.FileRead),
		FileWrite:    norm(g.FileWrite),
		AllowPrivate: g.AllowPrivate,
	})
	if err != nil {
		return nil, ErrBadShape
	}
	out := make([]byte, 0, len(domain)+len(body))
	out = append(out, domain...)
	out = append(out, body...)
	return out, nil
}

// Sign returns the sash/1 JSON document, including sig.
// The private key is the fleet key or the per-host emergency key.
// Sign does not decide policy; Verify does.
func Sign(g Grant, priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, ErrBadSig
	}
	msg, err := Message(g)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, msg)
	doc, err := json.Marshal(wire{
		signed: signed{
			V:            g.V,
			Actor:        g.Actor,
			Host:         g.Host,
			Purpose:      g.Purpose,
			Exp:          g.Exp,
			LeaseID:      g.LeaseID,
			Entitlements: norm(g.Entitlements),
			FileRead:     norm(g.FileRead),
			FileWrite:    norm(g.FileWrite),
			AllowPrivate: g.AllowPrivate,
		},
		Sig: base64.StdEncoding.EncodeToString(sig),
	})
	if err != nil {
		return nil, ErrBadShape
	}
	return doc, nil
}

// Verify checks doc against the fleet and emergency public keys.
// purpose inside the signed bytes selects the key. now is the caller's clock.
func Verify(doc []byte, fleet, emergency ed25519.PublicKey, now interface{ Unix() int64 }) (Grant, error) {
	if len(doc) == 0 {
		return Grant{}, ErrMissing
	}
	if len(doc) > MaxDocument {
		return Grant{}, ErrBadShape
	}
	g, sig, err := parse(doc)
	if err != nil {
		return Grant{}, err
	}
	if !distinctKeys(fleet, emergency) {
		return Grant{}, ErrBadSig
	}
	key := fleet
	if g.Purpose == PurposeEmergency {
		key = emergency
	}
	raw, decErr := base64.StdEncoding.DecodeString(sig)
	if decErr != nil || len(raw) != ed25519.SignatureSize {
		return Grant{}, ErrBadSig
	}
	msg, err := Message(g)
	if err != nil {
		return Grant{}, ErrBadSig
	}
	if !ed25519.Verify(key, msg, raw) {
		return Grant{}, ErrBadSig
	}
	for _, name := range g.Entitlements {
		if name == "*" {
			return Grant{}, ErrStar
		}
	}
	if g.Exp < now.Unix() {
		return Grant{}, ErrExpired
	}
	return g, nil
}

func parse(doc []byte) (Grant, string, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	var w wire
	if err := dec.Decode(&w); err != nil {
		return Grant{}, "", ErrBadShape
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Grant{}, "", ErrBadShape
	}
	if err := shape(doc, w); err != nil {
		return Grant{}, "", err
	}
	return Grant{
		V:            w.V,
		Actor:        w.Actor,
		Host:         w.Host,
		Purpose:      w.Purpose,
		Exp:          w.Exp,
		LeaseID:      w.LeaseID,
		Entitlements: append([]string(nil), w.Entitlements...),
		FileRead:     append([]string(nil), w.FileRead...),
		FileWrite:    append([]string(nil), w.FileWrite...),
		AllowPrivate: w.AllowPrivate,
	}, w.Sig, nil
}

func shape(doc []byte, w wire) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		return ErrBadShape
	}
	for _, key := range []string{"v", "actor", "host", "purpose", "exp", "lease_id", "entitlements", "sig"} {
		if _, ok := raw[key]; !ok {
			return ErrBadShape
		}
	}
	if w.V != "sash/1" || w.Actor == "" || w.Host == "" || w.LeaseID == "" || w.Sig == "" {
		return ErrBadShape
	}
	switch w.Purpose {
	case PurposeOpen, PurposeRoot, PurposeEmergency:
	default:
		return ErrBadShape
	}
	if w.Entitlements == nil {
		return ErrBadShape
	}
	for _, name := range w.Entitlements {
		if name == "" {
			return ErrBadShape
		}
	}
	if err := roots(w.FileRead); err != nil {
		return err
	}
	return roots(w.FileWrite)
}

func roots(list []string) error {
	for _, r := range list {
		if path.Clean(r) != r || !strings.HasPrefix(r, "/") {
			return ErrBadShape
		}
	}
	return nil
}

func distinctKeys(a, b ed25519.PublicKey) bool {
	if len(a) != ed25519.PublicKeySize || len(b) != ed25519.PublicKeySize {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 0
}

func norm(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
