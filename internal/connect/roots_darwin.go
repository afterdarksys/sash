//go:build darwin

package connect

import (
	"crypto/x509"
	"os"
)

// loadPlatformRoots makes verification the platform verifier's
// (Security.framework, the keychain's trust settings). Since Go 1.27,
// SSL_CERT_FILE or SSL_CERT_DIR in the environment when crypto/x509 first
// loads its system roots replaces that verifier with the named files (on
// by default for a go line of 1.27 or later, and for any go line when
// GODEBUG=x509sslcertoverrideplatform=1 is in the environment), and there
// is no way to ask for the platform verifier with an explicit pool.
// So both names are removed from this process's environment, then the
// system roots are loaded at once, before any connection: they load once
// per process, so nothing after this can change them. A nil pool is the
// platform verifier. Residual: a GODEBUG or a caller in this process that
// loaded the system roots earlier; sash connect loads nothing before it.
func loadPlatformRoots() *x509.CertPool {
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if err := os.Unsetenv(name); err != nil {
			return x509.NewCertPool()
		}
	}
	if _, err := x509.SystemCertPool(); err != nil {
		return x509.NewCertPool()
	}
	return nil
}
