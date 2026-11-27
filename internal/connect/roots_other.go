//go:build !darwin && !linux

package connect

import "crypto/x509"

// loadPlatformRoots is an empty pool here: sash connect refuses the token
// file on this platform before any connection, and would fail closed if
// it got that far.
func loadPlatformRoots() *x509.CertPool { return x509.NewCertPool() }
