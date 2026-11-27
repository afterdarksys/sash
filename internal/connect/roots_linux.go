//go:build linux

package connect

import (
	"crypto/x509"
	"os"
	"path/filepath"
)

// The platform's bundle paths, as crypto/x509 lists them for Linux: the
// first file that exists, and every file in each directory.
var (
	rootFiles = []string{
		"/etc/ssl/certs/ca-certificates.crt",                // Debian, Ubuntu, Gentoo
		"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora, RHEL 6
		"/etc/ssl/ca-bundle.pem",                            // OpenSUSE
		"/etc/pki/tls/cacert.pem",                           // OpenELEC
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // CentOS, RHEL 7
		"/etc/ssl/cert.pem",                                 // Alpine
	}
	rootDirs = []string{"/etc/ssl/certs", "/etc/pki/tls/certs"}
)

// loadPlatformRoots reads the platform's roots from its own bundle paths
// into a pool of sash's own, so SSL_CERT_FILE and SSL_CERT_DIR, which
// crypto/x509 lets replace the system roots, never reach the pool. No
// root found is an empty pool: every connection then fails closed.
func loadPlatformRoots() *x509.CertPool {
	pool := x509.NewCertPool()
	for _, file := range rootFiles {
		if data, err := os.ReadFile(file); err == nil {
			pool.AppendCertsFromPEM(data)
			break
		}
	}
	for _, dir := range rootDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() && e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				pool.AppendCertsFromPEM(data)
			}
		}
	}
	return pool
}
