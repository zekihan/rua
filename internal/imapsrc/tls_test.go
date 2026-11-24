package imapsrc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImplicitTLS(t *testing.T) {
	t.Parallel()

	built := tlsRig(t, tlsImplicit, true)
	built.create(t, "Reports")
	built.deliver(t, "Reports", "secure", "body", time.Unix(1_700_001_000, 0).UTC())

	err := built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	built.assertReadOnly(t, "Reports")

	if len(built.sink.uids()) != 1 {
		t.Fatalf("uids = %v", built.sink.uids())
	}
}

func TestSTARTTLS(t *testing.T) {
	t.Parallel()

	built := tlsRig(t, tlsStart, true)
	built.create(t, "Reports")
	built.deliver(t, "Reports", "secure", "body", time.Unix(1_700_001_100, 0).UTC())

	err := built.fetch(t)
	if err != nil {
		t.Fatal(err)
	}

	built.assertReadOnly(t, "Reports")

	if len(built.sink.uids()) != 1 {
		t.Fatalf("uids = %v", built.sink.uids())
	}
}

func TestImplicitTLSRejectsUnknownCA(t *testing.T) {
	t.Parallel()

	built := tlsRig(t, tlsImplicit, false)
	built.create(t, "Reports")
	built.deliver(t, "Reports", "secure", "body", time.Unix(1_700_001_200, 0).UTC())

	clock := newFakeClock(time.Unix(1_800_001_200, 0).UTC())
	clock.install(built.fetcher)

	errCh := make(chan error, 1)

	go func() {
		errCh <- built.fetcher.FetchOnce(t.Context())
	}()

	for _, want := range connectBackoff {
		got := clock.read(t)
		if got != want {
			t.Fatalf("sleep %s, want %s", got, want)
		}

		clock.Advance(got)
	}

	err := waitErr(t, errCh)
	if err == nil {
		t.Fatal("expected TLS error")
	}

	clock.expectIdle(t)

	if len(built.sink.uids()) != 0 {
		t.Fatalf("uids = %v", built.sink.uids())
	}
}

func TestConnectionTLSRejectsBadCA(t *testing.T) {
	t.Parallel()

	cfg := filledConfig(
		"127.0.0.1:993", "", filepath.Join(t.TempDir(), "missing.pem"), "", "", nil, 0, 0,
	)

	_, err := connectionTLS(&cfg)
	if err == nil {
		t.Fatal("expected missing file")
	}

	path := filepath.Join(t.TempDir(), "bad.pem")

	writeErr := os.WriteFile(path, []byte("not a certificate"), 0o600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	bad := filledConfig("127.0.0.1:993", "", path, "", "", nil, 0, 0)

	_, err = connectionTLS(&bad)
	if !errors.Is(err, errCAParse) {
		t.Fatalf("err = %v", err)
	}
}

func TestPlaintextRequiresLoopback(t *testing.T) {
	t.Parallel()

	remote := filledConfig("example.com:993", tlsNone, "", "", "", nil, 0, 0)

	_, err := dialIMAP(t.Context(), &remote)
	if !errors.Is(err, errPlainLoopback) {
		t.Fatalf("example.com: %v", err)
	}

	numeric := filledConfig("8.8.8.8:993", tlsNone, "", "", "", nil, 0, 0)

	_, err = dialIMAP(t.Context(), &numeric)
	if !errors.Is(err, errPlainLoopback) {
		t.Fatalf("address: %v", err)
	}

	badMode := filledConfig("127.0.0.1:993", "plain", "", "", "", nil, 0, 0)

	_, err = dialIMAP(t.Context(), &badMode)
	if !errors.Is(err, errTLSMode) {
		t.Fatalf("mode: %v", err)
	}

	for _, addr := range []string{"127.0.0.1", "127.0.0.1:143", "[::1]:143", "localhost", "localhost:143"} {
		if !loopbackHost(addr) {
			t.Fatalf("%s is loopback", addr)
		}
	}

	for _, addr := range []string{"8.8.8.8", "8.8.8.8:993", "example.com", "example.com:993"} {
		if loopbackHost(addr) {
			t.Fatalf("%s is not loopback", addr)
		}
	}
}

func tlsRig(t *testing.T, mode string, trust bool) *rig {
	t.Helper()

	cert, caFile := testCert(t)
	if !trust {
		caFile = ""
	}

	tlsConfig := new(tls.Config)
	tlsConfig.Certificates = []tls.Certificate{cert}
	tlsConfig.MinVersion = tls.VersionTLS12

	return newServerRig(t, []string{"Reports"}, serverSpec{
		mode:   mode,
		caFile: caFile,
		config: tlsConfig,
	})
}

func testCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := new(x509.Certificate)
	template.SerialNumber = big.NewInt(1)
	template.NotBefore = time.Now().Add(-time.Hour)
	template.NotAfter = time.Now().Add(24 * time.Hour)
	template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	template.BasicConstraintsValid = true
	template.IsCA = true

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certBlock := new(pem.Block)
	certBlock.Type = "CERTIFICATE"
	certBlock.Bytes = der
	certPEM := pem.EncodeToMemory(certBlock)

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	keyBlock := new(pem.Block)
	keyBlock.Type = "PRIVATE KEY"
	keyBlock.Bytes = keyDER
	keyPEM := pem.EncodeToMemory(keyBlock)

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "ca.pem")

	err = os.WriteFile(path, certPEM, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return cert, path
}
