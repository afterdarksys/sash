package grant

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strings"
)

// Threats: a forged or swapped sash/1 grant, a lease signature presented as a
// grant, a fleet signature on purpose emergency (and the reverse), a "*", an
// expired exp, a relative or unclean file root, identical purpose keys, and
// an oversized document. Verification fails closed before the prompt.
// sash/2 (the boot grant, purpose service) signs under its own domain
// prefix, so a sash/1 signature never verifies as sash/2 or the reverse;
// exp 0 ("never") is allowed only for purpose service.
// Does NOT hold a private key. Does NOT treat systemapi-lease-v1 or
// "<action>\n<payload>" as a grant message. Does NOT open a pubkey path;
// the caller passes the raw 32-byte keys.

const (
	// MaxDocument is the largest grant sash will read.
	MaxDocument = 64 * 1024

	PurposeOpen      = "terminal:open"
	PurposeRoot      = "terminal:root"
	PurposeEmergency = "emergency"
	// PurposeService is a sash/2 boot grant for one service, signed by the
	// fleet key.
	PurposeService = "service"

	domain   = "sash-grant-v1\n"
	domainV2 = "sash-grant-v2\n"
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
	// ErrRootSlash is a file root that is the whole filesystem: "/", or
	// anything that cleans to it ("//", "/./", "/.."). sash refuses it,
	// so no grant hands a session every path; the error is a fixed code
	// and never echoes the root.
	ErrRootSlash Error = "grant_root_slash"
	// ErrExpZero is exp 0 ("never") on a purpose other than service.
	ErrExpZero Error = "grant_exp_zero"
	// ErrServiceMismatch is a service file run under a grant for another
	// service (or no service at all). It is checked before anything runs.
	ErrServiceMismatch Error = "grant_service_mismatch"
	// ErrIdentityMismatch is a service block whose user or group is not
	// the grant's.
	ErrIdentityMismatch Error = "grant_identity_mismatch"
)

// ServiceName is the form of a service name, in the grant and the service
// block alike.
var ServiceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// UnixName is a user or group name.
var UnixName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// MountName is a mount source that is a filesystem name, not a path.
var MountName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

var capName = regexp.MustCompile(`^CAP_[A-Z0-9_]{1,32}$`)

// CheckService is nil when g is a sash/2 service grant for name.
func CheckService(g Grant, name string) error {
	if !g.Boot() || g.Service == "" || g.Service != name {
		return ErrServiceMismatch
	}
	return nil
}

// CheckIdentity is nil when the service block's user and group are the
// grant's. An unset block identity means the grant's.
func CheckIdentity(g Grant, user, group string) error {
	if (user != "" && user != g.User) || (group != "" && group != g.Group) {
		return ErrIdentityMismatch
	}
	return nil
}

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

// signedV2 is every sash/1 field, then the sash/2 fields in the order the
// contract fixes: service, exec, caps, mounts, serial.
type signedV2 struct {
	signed
	Service string   `json:"service"`
	Exec    []string `json:"exec"`
	Caps    []string `json:"caps"`
	Mounts  []string `json:"mounts"`
	// MountSources are what mount may mount there.
	MountSources []string `json:"mount_sources"`
	User         string   `json:"user"`
	Group        string   `json:"group"`
	Serial       uint64   `json:"serial"`
}

type wireV2 struct {
	signedV2
	Sig string `json:"sig"`
}

func v1Fields(g Grant) signed {
	return signed{
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
	}
}

func v2Fields(g Grant) signedV2 {
	return signedV2{signed: v1Fields(g), Service: g.Service, Exec: norm(g.Exec), Caps: norm(g.Caps), Mounts: norm(g.Mounts), MountSources: norm(g.MountSources), User: g.User, Group: g.Group, Serial: g.Serial}
}

// Message is the exact byte string an ed25519 key signs for this grant:
// sash-grant-v1 and the sash/1 fields, or for V "sash/2" sash-grant-v2 and
// the sash/2 fields.
func Message(g Grant) ([]byte, error) {
	if g.V == "sash/2" {
		body, err := json.Marshal(v2Fields(g))
		if err != nil {
			return nil, ErrBadShape
		}
		return append([]byte(domainV2), body...), nil
	}
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

// Sign returns the sash/1 (or, for V "sash/2", the sash/2) JSON document,
// including sig.
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
	if g.V == "sash/2" {
		doc, err := json.Marshal(wireV2{signedV2: v2Fields(g), Sig: base64.StdEncoding.EncodeToString(sig)})
		if err != nil {
			return nil, ErrBadShape
		}
		return doc, nil
	}
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
	// exp 0 is never, and only a boot grant (purpose service) carries it.
	if g.Exp < now.Unix() && !(g.Purpose == PurposeService && g.Exp == 0) {
		return Grant{}, ErrExpired
	}
	return g, nil
}

func parse(doc []byte) (Grant, string, error) {
	var head struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(doc, &head); err != nil {
		return Grant{}, "", ErrBadShape
	}
	if string(head.V) == `"sash/2"` {
		return parseV2(doc)
	}
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
	return shapeCommon(w.signed)
}

// shapeCommon is the shape both versions share once v and purpose are
// known.
func shapeCommon(w signed) error {
	if w.Exp == 0 && w.Purpose != PurposeService {
		return ErrExpZero
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

// parseV2 reads a sash/2 document: purpose service only, its service
// name, actor service:<name>, absolute clean exec programs and mount
// points, capability names, and a serial of at least 1.
func parseV2(doc []byte) (Grant, string, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	var w wireV2
	if err := dec.Decode(&w); err != nil {
		return Grant{}, "", ErrBadShape
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Grant{}, "", ErrBadShape
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		return Grant{}, "", ErrBadShape
	}
	for _, key := range []string{"v", "actor", "host", "purpose", "exp", "lease_id", "entitlements", "service", "exec", "caps", "mounts", "mount_sources", "user", "group", "serial", "sig"} {
		if _, ok := raw[key]; !ok {
			return Grant{}, "", ErrBadShape
		}
	}
	if w.V != "sash/2" || w.Purpose != PurposeService || w.Host == "" || w.LeaseID == "" || w.Sig == "" {
		return Grant{}, "", ErrBadShape
	}
	if !ServiceName.MatchString(w.Service) || w.Actor != "service:"+w.Service || w.Serial < 1 || w.Exp < 0 {
		return Grant{}, "", ErrBadShape
	}
	if w.Exec == nil || w.Caps == nil || w.Mounts == nil || w.MountSources == nil {
		return Grant{}, "", ErrBadShape
	}
	if !UnixName.MatchString(w.User) || !UnixName.MatchString(w.Group) {
		return Grant{}, "", ErrBadShape
	}
	if err := shapeCommon(w.signed); err != nil {
		return Grant{}, "", err
	}
	for _, list := range [][]string{w.Exec, w.Mounts} {
		if err := roots(list); err != nil {
			return Grant{}, "", err
		}
	}
	for _, src := range w.MountSources {
		if MountName.MatchString(src) {
			continue
		}
		if err := roots([]string{src}); err != nil {
			return Grant{}, "", err
		}
	}
	for _, c := range w.Caps {
		if !capName.MatchString(c) {
			return Grant{}, "", ErrBadShape
		}
	}
	g := Grant{
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
		Service:      w.Service,
		Exec:         append([]string(nil), w.Exec...),
		Caps:         append([]string(nil), w.Caps...),
		Mounts:       append([]string(nil), w.Mounts...),
		MountSources: append([]string(nil), w.MountSources...),
		User:         w.User,
		Group:        w.Group,
		Serial:       w.Serial,
	}
	return g, w.Sig, nil
}

func roots(list []string) error {
	for _, r := range list {
		if strings.HasPrefix(r, "/") && path.Clean(r) == "/" {
			return ErrRootSlash
		}
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

// Request is the unsigned sash/2 body for g, in the signed field order,
// for a fleet signer to complete (host, lease_id, serial) and sign. sash
// never signs it.
func Request(g Grant) ([]byte, error) {
	g.V, g.Purpose = "sash/2", PurposeService
	b, err := json.MarshalIndent(v2Fields(g), "", "  ")
	if err != nil {
		return nil, ErrBadShape
	}
	return append(b, '\n'), nil
}
