package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// CA is an in-memory certificate authority that generates per-hostname TLS
// certificates on the fly. Used by the MITM proxy to terminate TLS from
// workspace containers so the pipeline can inspect and modify HTTPS traffic.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte // PEM-encoded CA certificate for injection into containers

	mu    sync.RWMutex
	cache map[string]*tls.Certificate // hostname → signed cert
}

// New generates a fresh ECDSA CA certificate. The CA is ephemeral — a new one
// is created each time the server starts. This is fine because workspace
// containers are also ephemeral and get the current CA cert injected at creation.
func New() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Cordon Proxy CA",
			Organization: []string{"Cordon"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour), // small backdate for clock skew
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	return &CA{
		cert:  cert,
		key:   key,
		pem:   certPEM,
		cache: make(map[string]*tls.Certificate),
	}, nil
}

// PEM returns the CA certificate in PEM format for injection into containers.
func (ca *CA) PEM() []byte {
	return ca.pem
}

// CertForHost returns a TLS certificate for the given hostname, signed by
// this CA. Certificates are cached so repeated connections to the same host
// reuse the same cert.
func (ca *CA) CertForHost(hostname string) (*tls.Certificate, error) {
	ca.mu.RLock()
	if cert, ok := ca.cache[hostname]; ok {
		ca.mu.RUnlock()
		return cert, nil
	}
	ca.mu.RUnlock()

	// Generate a new cert for this hostname
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating host key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("signing host certificate: %w", err)
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{certDER, ca.cert.Raw},
		PrivateKey:  key,
	}

	ca.mu.Lock()
	ca.cache[hostname] = tlsCert
	ca.mu.Unlock()

	return tlsCert, nil
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
