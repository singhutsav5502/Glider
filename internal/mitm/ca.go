package mitm

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/fileacl"
)

// Authority is a local MITM CA that mints per-host leaf certificates.
type Authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	tlsCert tls.Certificate
	mu      sync.Mutex
	cache   map[string]*tls.Certificate
}

// LoadOrCreateAuthority loads CA from disk or creates a new one.
func LoadOrCreateAuthority(certPath, keyPath string) (*Authority, error) {
	if certPath != "" && keyPath != "" {
		if _, err := os.Stat(certPath); err == nil {
			if _, err := os.Stat(keyPath); err == nil {
				return LoadAuthority(certPath, keyPath)
			}
		}
	}
	a, err := GenerateAuthority()
	if err != nil {
		return nil, err
	}
	if certPath != "" && keyPath != "" {
		if err := a.Save(certPath, keyPath); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// GenerateAuthority creates a new self-signed CA in memory.
func GenerateAuthority() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Glider MITM CA"},
			CommonName:   "Glider Local MITM CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	tlsCert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        cert,
	}
	return &Authority{
		cert:    cert,
		key:     key,
		tlsCert: tlsCert,
		cache:   map[string]*tls.Certificate{},
	}, nil
}

// LoadAuthority loads an existing CA from PEM files.
func LoadAuthority(certPath, keyPath string) (*Authority, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if len(tlsCert.Certificate) == 0 {
		return nil, fmt.Errorf("empty CA certificate")
	}
	leaf, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, err
	}
	tlsCert.Leaf = leaf
	key, ok := tlsCert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("CA key must be ECDSA")
	}
	return &Authority{
		cert:    leaf,
		key:     key,
		tlsCert: tlsCert,
		cache:   map[string]*tls.Certificate{},
	}, nil
}

// Save writes CA cert and key PEM to disk.
func (a *Authority) Save(certPath, keyPath string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.cert.Raw})
	keyDER, err := x509.MarshalECPrivateKey(a.key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	// This operation can fail, and that is acceptable.
	//
	// A person who takes the private key of the CA can do a MITM operation against
	// each host that trusts this CA. Therefore this file gets a true limit with a
	// Windows ACL, from internal/fileacl. The mode bits 0o600 above are not
	// sufficient, because Windows does not enforce them as a limit on access, and
	// Unix does.
	//
	// The code writes the failure to the log, and it does not return an error. The
	// key is already safe on the disk at this point.
	//
	// To stop the full setup of the CA because one ACL step could not operate would
	// be a worse result. An example of that condition is icacls that is absent from
	// the PATH. That must not occur on a true Windows machine, but it is not
	// sufficient cause to stop the program. The other result is "the file exists,
	// and its access is a small quantity wider than the intent".
	if err := fileacl.RestrictToCurrentUser(keyPath); err != nil {
		slog.Default().Warn("mitm: could not restrict CA key file ACL", "path", keyPath, "err", err)
	}
	return nil
}

// CertPEM returns the CA certificate in PEM form (for trust install / NODE_EXTRA_CA_CERTS).
func (a *Authority) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.cert.Raw})
}

// CertificateForHost returns a tls.Certificate for the given hostname (cached).
func (a *Authority) CertificateForHost(host string) (*tls.Certificate, error) {
	host = stripPort(host)
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.cache[host]; ok {
		return c, nil
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Glider MITM"},
			CommonName:   host,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
		tmpl.DNSNames = nil
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &leafKey.PublicKey, a.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{
		Certificate: [][]byte{der, a.cert.Raw},
		PrivateKey:  leafKey,
		Leaf:        leaf,
	}
	a.cache[host] = c
	return c, nil
}
