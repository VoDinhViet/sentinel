package api

import (
	"crypto/tls"
	"fmt"
	"net"
)

// TLSListener makes ln speak HTTPS with the certificate and private key in the two PEM files, TLS 1.2
// or later. The files are read now, so a missing or mismatched pair is an error at startup and
// not a handshake that fails later; a renewed certificate takes effect when the daemon restarts.
func TLSListener(ln net.Listener, certFile, keyFile string) (net.Listener, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("api.tls_cert and api.tls_key: %w", err)
	}
	return tls.NewListener(ln, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"}, // Serve is plain net/http, which has no HTTP/2 here
	}), nil
}
