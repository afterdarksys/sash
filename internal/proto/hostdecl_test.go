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
		"oversized":            `{"devices": {}, "x": "` + strings.Repeat("a", 64*1024) + `"}`,
	} {
		if _, err := prototest.ParseHostDecl([]byte(doc)); err == nil || err.Error() != "host_decl_invalid" {
			t.Errorf("%s: %v", name, err)
		}
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
