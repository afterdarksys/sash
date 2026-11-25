package proto_test

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/proto/prototest"
)

// host.json is strict: every case is host_decl_invalid.
func TestHostDeclRefusals(t *testing.T) {
	if _, err := prototest.ParseHostDecl([]byte(prototest.HostFixture)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	dev := `"devices": {"d": {"path": "/dev/vdb", "kind": "block"}}`
	for name, doc := range map[string]string{
		"unknown top key":      `{"modules": {}}`,
		"unknown device key":   `{"devices": {"d": {"path": "/dev/vdb", "kind": "block", "major": 8}}}`,
		"unclean device path":  `{"devices": {"d": {"path": "/dev/../etc/shadow", "kind": "block"}}}`,
		"path outside /dev":    `{"devices": {"d": {"path": "/tmp/vdb", "kind": "block"}}}`,
		"bad kind":             `{"devices": {"d": {"path": "/dev/vdb", "kind": "fifo"}}}`,
		"unknown match":        `{"devices": {"d": {"path": "/dev/vdb", "kind": "block", "match": {"vendor": "x"}}}}`,
		"device name path":     `{"devices": {"a/b": {"path": "/dev/vdb", "kind": "block"}}}`,
		"duplicate target":     `{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "d"}, "b": {"target": "/data", "fstype": "xfs", "source": "d"}}}`,
		"undeclared source":    `{"mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "sda1"}}}`,
		"tmpfs from a device":  `{` + dev + `, "mounts": {"a": {"target": "/t", "fstype": "tmpfs", "source": "d"}}}`,
		"bad fstype":           `{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "overlay", "source": "d"}}}`,
		"bad option":           `{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "d", "options": ["x-bind"]}}}`,
		"size on ext4":         `{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "d", "options": ["size=1g"]}}}`,
		"root target":          `{"mounts": {"a": {"target": "/", "fstype": "tmpfs", "source": "tmpfs"}}}`,
		"public no requires":   `{"network": {"wan": {"kind": "static", "link": "52:54:00:aa:bb:cc", "addresses": ["203.0.113.7/24"]}}}`,
		"bad mac":              `{"network": {"lan": {"kind": "static", "link": "eth0", "addresses": ["10.0.0.2/24"]}}}`,
		"nine addresses":       `{"network": {"lan": {"kind": "static", "link": "52:54:00:aa:bb:cc", "addresses": ["10.0.0.1/24","10.0.0.2/24","10.0.0.3/24","10.0.0.4/24","10.0.0.5/24","10.0.0.6/24","10.0.0.7/24","10.0.0.8/24","10.0.0.9/24"]}}}`,
		"unit profile with ip": `{"network": {"d": {"kind": "unit", "unit": "dhcp", "addresses": ["10.0.0.2/24"]}}}`,
		"bridge kind":          `{"network": {"br": {"kind": "bridge"}}}`,
		"trailing":             prototest.HostFixture + ` {}`,
		// Valid apart from its size: the fixture padded with whitespace.
		"oversized": prototest.HostFixture + strings.Repeat(" ", 64*1024),
		// Strict keys: exact case, each once, at every level.
		"duplicate top key":     `{"devices": {}, "devices": {}}`,
		"duplicate device name": `{"devices": {"d": {"path": "/dev/vdb", "kind": "block"}, "d": {"path": "/dev/vdc", "kind": "block"}}}`,
		"duplicate field":       `{"devices": {"d": {"path": "/dev/vdb", "kind": "block", "path": "/dev/sda"}}}`,
		"duplicate match":       `{"devices": {"d": {"path": "/dev/vdb", "kind": "block", "match": {"serial": "A", "serial": "B"}}}}`,
		"duplicate route field": `{"network": {"lan": {"kind": "static", "link": "52:54:00:aa:bb:cc", "addresses": ["10.0.0.2/24"], "routes": [{"dst": "0.0.0.0/0", "gw": "10.0.0.1", "gw": "10.0.0.9"}]}}}`,
		"top key case":          `{"Devices": {}}`,
		"field key case":        `{"devices": {"d": {"PATH": "/dev/vdb", "kind": "block"}}}`,
		"route key case":        `{"network": {"lan": {"kind": "static", "link": "52:54:00:aa:bb:cc", "routes": [{"Dst": "0.0.0.0/0", "gw": "10.0.0.1"}]}}}`,
		"top null":              `null`,
		"null table":            `{"devices": null}`,
		"null entry":            `{"devices": {"d": null}}`,
		"null field":            `{"devices": {"d": {"path": null, "kind": "block"}}}`,
		"null mtu":              `{"network": {"lan": {"kind": "static", "link": "52:54:00:aa:bb:cc", "mtu": null, "addresses": ["10.0.0.2/24"]}}}`,
		"null option":           `{"mounts": {"a": {"target": "/t", "fstype": "tmpfs", "source": "tmpfs", "options": [null]}}}`,
		"device path space":     `{"devices": {"d": {"path": "/dev/v db", "kind": "block"}}}`,
		"device path unicode":   `{"devices": {"d": {"path": "/dev/vdb‮", "kind": "block"}}}`,
		"device path is /dev":   `{"devices": {"d": {"path": "/dev/", "kind": "block"}}}`,
		"ro and rw":             `{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "d", "options": ["ro", "rw"]}}}`,
		"suid and nosuid":       `{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "d", "options": ["suid", "nosuid"]}}}`,
		"dev and nodev":         `{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "d", "options": ["dev", "nodev"]}}}`,
	} {
		if _, err := prototest.ParseHostDecl([]byte(doc)); err == nil || err.Error() != "host_decl_invalid" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Each option of a conflicting pair is fine alone.
	if _, err := prototest.ParseHostDecl([]byte(`{` + dev + `, "mounts": {"a": {"target": "/data", "fstype": "ext4", "source": "d", "options": ["ro", "suid", "dev"]}}}`)); err != nil {
		t.Fatalf("one of each pair: %v", err)
	}
	many := `{"network": {`
	for i := 0; i < 65; i++ {
		if i > 0 {
			many += ","
		}
		many += `"n` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `": {"kind": "unit", "unit": "dhcp"}`
	}
	if _, err := prototest.ParseHostDecl([]byte(many + `}}`)); err == nil {
		t.Error("65 profiles")
	}
}
