package main

import (
	"net"
	"strings"
	"testing"

	"sentinel/internal/config"
)

func TestListenAPIWithoutACertificateIsPlain(t *testing.T) {
	ln, err := listenAPI(config.API{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().String() == "" {
		t.Error("no address")
	}
}

func TestListenAPIWithACertificateThatCannotBeReadFailsAndReleasesThePort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()

	_, err = listenAPI(config.API{Listen: address, TLSCert: "/nowhere/cert.pem", TLSKey: "/nowhere/key.pem"})
	if err == nil || !strings.Contains(err.Error(), "api.tls_cert") {
		t.Fatalf("error = %v, want one that names the settings", err)
	}
	again, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("the port was not released: %v", err)
	}
	again.Close()
}

func TestListenAPIReportsAPortThatIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if _, err := listenAPI(config.API{Listen: taken.Addr().String()}); err == nil || !strings.Contains(err.Error(), "api.listen") {
		t.Errorf("error = %v", err)
	}
}
