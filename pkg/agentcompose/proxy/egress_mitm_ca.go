package proxy

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// egressMITMCA is the daemon certificate authority the L7 TLS path signs one
// leaf per requested host with. It is created once during composition and owns
// its mutable leaf cache behind a lock, so concurrent tunnels cannot race on it.
//
// The CA is either loaded from an operator-supplied certificate and key or
// generated when the daemon starts. A generated CA is ephemeral: every restart
// produces a new one, so a deployed sandbox must be given a stable CA to trust.
type egressMITMCA struct {
	certificate *x509.Certificate
	signer      crypto.Signer
	now         func() time.Time

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

const (
	// egressMITMCALifetime bounds a generated CA. A generated CA is meant for a
	// test or a single daemon lifetime, not to outlive a deployment.
	egressMITMCALifetime = 30 * 24 * time.Hour
	// egressMITMLeafLifetime bounds one issued leaf. A cached leaf is reused
	// until shortly before it expires.
	egressMITMLeafLifetime = 24 * time.Hour
	// egressMITMLeafRenewBefore is how long before expiry a cached leaf is
	// reissued rather than reused.
	egressMITMLeafRenewBefore = 5 * time.Minute
)

// newEgressMITMCA loads the configured CA or generates an ephemeral one. Both
// PEM values must be supplied together; supplying neither is the explicit
// ephemeral request.
func newEgressMITMCA(certPEM, keyPEM []byte) (*egressMITMCA, error) {
	if len(bytes.TrimSpace(certPEM)) == 0 && len(bytes.TrimSpace(keyPEM)) == 0 {
		certificate, signer, err := generateEgressMITMCA()
		if err != nil {
			return nil, err
		}
		return &egressMITMCA{certificate: certificate, signer: signer, now: time.Now, leaves: make(map[string]*tls.Certificate)}, nil
	}
	if len(bytes.TrimSpace(certPEM)) == 0 || len(bytes.TrimSpace(keyPEM)) == 0 {
		return nil, errors.New("the L7 CA certificate and private key must be configured together")
	}
	certificate, signer, err := loadEgressMITMCA(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &egressMITMCA{certificate: certificate, signer: signer, now: time.Now, leaves: make(map[string]*tls.Certificate)}, nil
}

func generateEgressMITMCA() (*x509.Certificate, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate L7 CA key: %w", err)
	}
	serial, err := randomCertificateSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "agent-compose egress L7 CA", Organization: []string{"agent-compose"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(egressMITMCALifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, nil, fmt.Errorf("create L7 CA certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse generated L7 CA certificate: %w", err)
	}
	return certificate, key, nil
}

func loadEgressMITMCA(certPEM, keyPEM []byte) (*x509.Certificate, crypto.Signer, error) {
	certificate, err := parseEgressMITMCertificate(certPEM)
	if err != nil {
		return nil, nil, err
	}
	if !certificate.IsCA {
		return nil, nil, errors.New("the configured L7 CA certificate is not a CA: its basic constraints do not set is_ca")
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) {
		return nil, nil, fmt.Errorf("the configured L7 CA certificate is not valid before %s", certificate.NotBefore.Format(time.RFC3339))
	}
	if !now.Before(certificate.NotAfter) {
		return nil, nil, fmt.Errorf("the configured L7 CA certificate expired at %s", certificate.NotAfter.Format(time.RFC3339))
	}
	signer, err := parseEgressMITMCASigner(keyPEM)
	if err != nil {
		return nil, nil, err
	}
	signerPublic, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal the L7 CA private key's public key: %w", err)
	}
	certificatePublic, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal the L7 CA certificate's public key: %w", err)
	}
	if !bytes.Equal(signerPublic, certificatePublic) {
		return nil, nil, errors.New("the configured L7 CA private key does not match its certificate")
	}
	return certificate, signer, nil
}

func parseEgressMITMCertificate(value []byte) (*x509.Certificate, error) {
	for {
		block, rest := pem.Decode(value)
		if block == nil {
			return nil, errors.New("the configured L7 CA certificate is not PEM-encoded")
		}
		value = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse the configured L7 CA certificate: %w", err)
		}
		return certificate, nil
	}
}

func parseEgressMITMCASigner(value []byte) (crypto.Signer, error) {
	for {
		block, rest := pem.Decode(value)
		if block == nil {
			return nil, errors.New("the configured L7 CA private key is not PEM-encoded")
		}
		value = rest
		switch block.Type {
		case "PRIVATE KEY":
			parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse the configured PKCS#8 L7 CA private key: %w", err)
			}
			signer, ok := parsed.(crypto.Signer)
			if !ok {
				return nil, errors.New("the configured L7 CA private key cannot sign certificates")
			}
			return signer, nil
		case "EC PRIVATE KEY":
			parsed, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse the configured EC L7 CA private key: %w", err)
			}
			return parsed, nil
		case "RSA PRIVATE KEY":
			parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse the configured RSA L7 CA private key: %w", err)
			}
			return parsed, nil
		}
	}
}

// certificateFor returns a cached leaf for host, issuing one when the cache has
// no valid leaf. The returned certificate is owned by the cache and must not be
// mutated by a caller.
func (a *egressMITMCA) certificateFor(host string) (*tls.Certificate, error) {
	normalized := normalizeMITMHost(host)
	if normalized == "" {
		return nil, errors.New("the L7 TLS path has no host to issue a certificate for")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if leaf, ok := a.leaves[normalized]; ok && leaf.Leaf != nil && a.now().Add(egressMITMLeafRenewBefore).Before(leaf.Leaf.NotAfter) {
		return leaf, nil
	}
	leaf, err := a.issueLeaf(normalized)
	if err != nil {
		return nil, err
	}
	a.leaves[normalized] = leaf
	return leaf, nil
}

func (a *egressMITMCA) issueLeaf(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate a leaf key for %s: %w", host, err)
	}
	serial, err := randomCertificateSerial()
	if err != nil {
		return nil, err
	}
	now := a.now()
	notAfter := now.Add(egressMITMLeafLifetime)
	if notAfter.After(a.certificate.NotAfter) {
		notAfter = a.certificate.NotAfter
	}
	if !notAfter.After(now) {
		return nil, fmt.Errorf("the L7 CA certificate expired at %s", a.certificate.NotAfter.Format(time.RFC3339))
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if literal := net.ParseIP(host); literal != nil {
		template.IPAddresses = []net.IP{literal}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, key.Public(), a.signer)
	if err != nil {
		return nil, fmt.Errorf("sign a leaf certificate for %s: %w", host, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse the signed leaf certificate for %s: %w", host, err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, a.certificate.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// CACertificatePEM returns the CA certificate the proxy signs with. It is public
// material: an operator or test uses it to trust the leaves.
func (a *egressMITMCA) CACertificatePEM() []byte {
	if a == nil || a.certificate == nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.certificate.Raw})
}

func normalizeMITMHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]")), ".")
}

func randomCertificateSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate a certificate serial number: %w", err)
	}
	return serial.Add(serial, big.NewInt(1)), nil
}
