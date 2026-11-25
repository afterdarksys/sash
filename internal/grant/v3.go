package grant

// sash/3: the boot grant (docs/plans/2026-11-24-sash-boot-verbs-contract.md,
// "The grant: sash/3").
//
// Threats: a sash/1 or sash/2 grant (or a lease) presented as a boot grant,
// a boot grant presented as a service grant, a tampered or re-signed list,
// a grant for another service (actor), a name that is a path, an alias or a
// module parameter (../x, a/b, x.ko, virtio-net, fs-ext4), a list padded
// past 64 entries or with duplicates, ensure smuggled in as a mount source
// so mount ensure could hide a real mount, and an oversized document. Every
// sash/3 field is signed under its own domain prefix, sash-grant-v3, so no
// sash/1 or sash/2 signature verifies as sash/3 and no sash/3 signature as
// either; purpose boot is sash/3 only and sash/3 is purpose boot only.
// Every field is required, unknown fields and trailing data are refused,
// and each check fails closed with grant_bad_shape before the signature
// is used. Does NOT check the host binding (the init compares host with its
// trust file's host_id), the serial floor (the init's) or what a name means
// (the init's host.json, module tree and catalog): a grant names, the init
// declares. Does NOT hold a private key.

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

const (
	// PurposeBoot is a sash/3 boot grant for one boot-ops unit, signed by
	// the fleet key. It carries the boot verbs and the service verbs.
	PurposeBoot = "boot"

	domainV3 = "sash-grant-v3\n"

	// MaxBootList is the most entries one sash/3 name list may hold.
	MaxBootList = 64
)

// The sash/3 name grammars. None holds "/", "." or a leading "-", so no
// name can be a path, a file name or a flag.
var (
	// ModuleName is the kernel's canonical spelling: "_", never "-".
	ModuleName = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,54}$`)
	// BootName is a device, host mount or network profile name.
	BootName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	// UnitName is a sahinit unit name.
	UnitName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// mountSourceEnsure is refused as a mount source in every version: a mount
// stage whose second word is ensure is always mount ensure.
const mountSourceEnsure = "ensure"

// signedV3 is every sash/2 field but serial, then the six boot lists, then
// serial last, in the order the contract fixes.
type signedV3 struct {
	signed
	Service      string   `json:"service"`
	Exec         []string `json:"exec"`
	Caps         []string `json:"caps"`
	Mounts       []string `json:"mounts"`
	MountSources []string `json:"mount_sources"`
	User         string   `json:"user"`
	Group        string   `json:"group"`
	EnvNames     []string `json:"env_names"`
	Modules      []string `json:"modules"`
	Devices      []string `json:"devices"`
	HostMounts   []string `json:"host_mounts"`
	NetProfiles  []string `json:"net_profiles"`
	UnitsStart   []string `json:"units_start"`
	UnitsStop    []string `json:"units_stop"`
	Serial       uint64   `json:"serial"`
}

type wireV3 struct {
	signedV3
	Sig string `json:"sig"`
}

func v3Fields(g Grant) signedV3 {
	return signedV3{
		signed: v1Fields(g), Service: g.Service, Exec: norm(g.Exec), Caps: norm(g.Caps), Mounts: norm(g.Mounts),
		MountSources: norm(g.MountSources), User: g.User, Group: g.Group, EnvNames: norm(g.EnvNames),
		Modules: norm(g.Modules), Devices: norm(g.Devices), HostMounts: norm(g.HostMounts),
		NetProfiles: norm(g.NetProfiles), UnitsStart: norm(g.UnitsStart), UnitsStop: norm(g.UnitsStop),
		Serial: g.Serial,
	}
}

func messageV3(g Grant) ([]byte, error) {
	body, err := json.Marshal(v3Fields(g))
	if err != nil {
		return nil, ErrBadShape
	}
	return append([]byte(domainV3), body...), nil
}

// v3Keys are every field a sash/3 document must carry.
var v3Keys = []string{"v", "actor", "host", "purpose", "exp", "lease_id", "entitlements", "file_read", "file_write", "allow_private",
	"service", "exec", "caps", "mounts", "mount_sources", "user", "group", "env_names",
	"modules", "devices", "host_mounts", "net_profiles", "units_start", "units_stop", "serial", "sig"}

// parseV3 reads a sash/3 document: purpose boot only, actor boot:<service>,
// every sash/2 rule, and the six name lists (each at most 64 entries, no
// duplicates, every entry in its grammar).
func parseV3(doc []byte) (Grant, string, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	var w wireV3
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
	for _, key := range v3Keys {
		if _, ok := raw[key]; !ok {
			return Grant{}, "", ErrBadShape
		}
	}
	if w.V != "sash/3" || w.Purpose != PurposeBoot || w.Host == "" || w.LeaseID == "" || w.Sig == "" {
		return Grant{}, "", ErrBadShape
	}
	if !ServiceName.MatchString(w.Service) || w.Actor != "boot:"+w.Service || w.Serial < 1 || w.Exp < 0 {
		return Grant{}, "", ErrBadShape
	}
	if !UnixName.MatchString(w.User) || !UnixName.MatchString(w.Group) {
		return Grant{}, "", ErrBadShape
	}
	if err := shapeCommon(w.signed); err != nil {
		return Grant{}, "", err
	}
	if err := shapeServiceLists(w.Exec, w.Caps, w.Mounts, w.MountSources, w.EnvNames); err != nil {
		return Grant{}, "", err
	}
	for _, l := range []struct {
		list []string
		re   *regexp.Regexp
	}{
		{w.Modules, ModuleName}, {w.Devices, BootName}, {w.HostMounts, BootName},
		{w.NetProfiles, BootName}, {w.UnitsStart, UnitName}, {w.UnitsStop, UnitName},
	} {
		if !bootList(l.list, l.re) {
			return Grant{}, "", ErrBadShape
		}
	}
	cp := func(s []string) []string { return append([]string(nil), s...) }
	return Grant{
		V: w.V, Actor: w.Actor, Host: w.Host, Purpose: w.Purpose, Exp: w.Exp, LeaseID: w.LeaseID,
		Entitlements: cp(w.Entitlements), FileRead: cp(w.FileRead), FileWrite: cp(w.FileWrite), AllowPrivate: w.AllowPrivate,
		Service: w.Service, Exec: cp(w.Exec), Caps: cp(w.Caps), Mounts: cp(w.Mounts), MountSources: cp(w.MountSources),
		User: w.User, Group: w.Group, EnvNames: cp(w.EnvNames),
		Modules: cp(w.Modules), Devices: cp(w.Devices), HostMounts: cp(w.HostMounts),
		NetProfiles: cp(w.NetProfiles), UnitsStart: cp(w.UnitsStart), UnitsStop: cp(w.UnitsStop),
		Serial: w.Serial,
	}, w.Sig, nil
}

// bootList is a present list of at most MaxBootList entries, each in re,
// none twice.
func bootList(list []string, re *regexp.Regexp) bool {
	if list == nil || len(list) > MaxBootList {
		return false
	}
	seen := map[string]bool{}
	for _, s := range list {
		if !re.MatchString(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
