package app

import (
	"crypto/tls"
	"crypto/x509"

	"sentinel/pkg/pki"
)

func tlsPair(chain, key []byte) (tls.Certificate, error) { return tls.X509KeyPair(chain, key) }

func verifyOptions(ca *pki.CA) x509.VerifyOptions {
	return x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
}
