package fleet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The agent's TLS identity is a self-signed certificate minted on first start and pinned by its
// fingerprint at pairing: the hub verifies the leaf's SHA-256 and nothing else — not the name,
// not the validity dates — so a pin never expires and no CA is involved. Ten years of validity
// only keeps strict clients (curl, for a look) from complaining.

// Pin is the SHA-256 of a certificate's DER bytes, 64 lowercase hex.
func Pin(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// LoadOrCreateCert reads the certificate and key at the given paths, minting both (key 0600)
// when the certificate is absent. It returns the certificate for the listener and its pin.
func LoadOrCreateCert(certPath, keyPath string) (tls.Certificate, string, error) {
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := createCert(certPath, keyPath); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("agent tls: %w (delete %s and %s to mint a new pair)", err, certPath, keyPath)
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, "", errors.New("agent tls: empty certificate")
	}
	return cert, Pin(cert.Certificate[0]), nil
}

// CertPin reads the pin of an existing certificate file (`todobem agent pair` needs it).
func CertPin(certPath string) (string, error) {
	b, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("%s: not a PEM certificate", certPath)
	}
	return Pin(block.Bytes), nil
}

func createCert(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "todobem-agent"
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// ServerTLS is the listener's configuration: the agent's certificate, TLS 1.3 only.
func ServerTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
}

// PinnedTransport dials with TLS and accepts exactly one server: the certificate whose pin is
// given. Everything the standard verifier would check is skipped on purpose (the pin is the
// whole trust), and the transport keeps one idle connection per agent alive.
func PinnedTransport(pin string) *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 2,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, // the pin below is the verification
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate presented")
				}
				if got := Pin(raw[0]); got != pin {
					return fmt.Errorf("certificate pin %s… does not match the paired %s…", got[:12], pin[:12])
				}
				return nil
			},
		},
	}
}
