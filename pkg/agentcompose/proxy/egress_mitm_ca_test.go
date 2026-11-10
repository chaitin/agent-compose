package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func testPoolFromPEM(t *testing.T, value []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(value) {
		t.Fatal("appending the certificate to a pool failed")
	}
	return pool
}

func TestEgressMITMCAGeneratesTrustedLeaves(t *testing.T) {
	authority, err := newEgressMITMCA(nil, nil)
	if err != nil {
		t.Fatalf("newEgressMITMCA() error = %v", err)
	}
	leaf, err := authority.certificateFor("api.example.com")
	if err != nil {
		t.Fatalf("certificateFor() error = %v", err)
	}
	if leaf.Leaf == nil {
		t.Fatal("issued leaf has no parsed certificate")
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		DNSName: "api.example.com",
		Roots:   testPoolFromPEM(t, authority.CACertificatePEM()),
	}); err != nil {
		t.Fatalf("verifying the generated leaf: %v", err)
	}
	// The cache returns the same certificate rather than signing a new one for
	// every request on the same host.
	cached, err := authority.certificateFor("API.EXAMPLE.COM")
	if err != nil {
		t.Fatalf("certificateFor() error = %v", err)
	}
	if cached != leaf {
		t.Fatal("the CA did not reuse the cached leaf for the same normalized host")
	}
}

func TestEgressMITMCAIssuesALeafForAnIPLiteral(t *testing.T) {
	authority, err := newEgressMITMCA(nil, nil)
	if err != nil {
		t.Fatalf("newEgressMITMCA() error = %v", err)
	}
	leaf, err := authority.certificateFor("192.0.2.10")
	if err != nil {
		t.Fatalf("certificateFor() error = %v", err)
	}
	if len(leaf.Leaf.IPAddresses) != 1 || leaf.Leaf.IPAddresses[0].String() != "192.0.2.10" {
		t.Fatalf("IP SANs = %v, want the destination address", leaf.Leaf.IPAddresses)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		DNSName: "192.0.2.10",
		Roots:   testPoolFromPEM(t, authority.CACertificatePEM()),
	}); err != nil {
		t.Fatalf("verifying the IP leaf: %v", err)
	}
}

func TestEgressMITMCALoadsAConfiguredCA(t *testing.T) {
	generated, err := newEgressMITMCA(nil, nil)
	if err != nil {
		t.Fatalf("generating the CA: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(generated.signer)
	if err != nil {
		t.Fatalf("marshalling the CA key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	loaded, err := newEgressMITMCA(generated.CACertificatePEM(), keyPEM)
	if err != nil {
		t.Fatalf("newEgressMITMCA(configured) error = %v", err)
	}
	leaf, err := loaded.certificateFor("api.example.com")
	if err != nil {
		t.Fatalf("certificateFor() error = %v", err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		DNSName: "api.example.com",
		Roots:   testPoolFromPEM(t, generated.CACertificatePEM()),
	}); err != nil {
		t.Fatalf("verifying a leaf signed by the loaded CA: %v", err)
	}
}

func TestEgressMITMCARejectsMismatchedOrPartialConfiguration(t *testing.T) {
	first, err := newEgressMITMCA(nil, nil)
	if err != nil {
		t.Fatalf("generating the first CA: %v", err)
	}
	second, err := newEgressMITMCA(nil, nil)
	if err != nil {
		t.Fatalf("generating the second CA: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(second.signer)
	if err != nil {
		t.Fatalf("marshalling the second CA key: %v", err)
	}
	secondKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if _, err := newEgressMITMCA(first.CACertificatePEM(), secondKeyPEM); err == nil {
		t.Fatal("newEgressMITMCA accepted a key that does not match the certificate")
	}
	if _, err := newEgressMITMCA(first.CACertificatePEM(), nil); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("newEgressMITMCA(partial) error = %v, want a configured-together rejection", err)
	}
	if _, err := newEgressMITMCA([]byte("not a certificate"), secondKeyPEM); err == nil {
		t.Fatal("newEgressMITMCA accepted a non-PEM certificate")
	}
}
