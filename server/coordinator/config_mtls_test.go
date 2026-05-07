package coordinator

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// genTestCert writes a self-signed cert + key + ca to dir. Returns the
// three file paths.
func genTestCert(t *testing.T, dir string) (certPath, keyPath, caPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("certgen: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	caPath = certPath // self-signed; CA is the same cert
	return
}

func TestLoadMTLSValidFiles(t *testing.T) {
	cert, key, ca := genTestCert(t, t.TempDir())
	cfg := &Config{
		URL:          "https://coord:8443",
		MTLSCertFile: cert,
		MTLSKeyFile:  key,
		MTLSCAFile:   ca,
	}
	tlsCfg, err := cfg.LoadMTLS()
	if err != nil {
		t.Fatalf("LoadMTLS: %v", err)
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("Certificates len = %d, want 1", len(tlsCfg.Certificates))
	}
	if tlsCfg.RootCAs == nil {
		t.Error("RootCAs is nil")
	}
}

func TestLoadMTLSMissingCertFile(t *testing.T) {
	cfg := &Config{
		URL:          "https://coord:8443",
		MTLSCertFile: "/nonexistent/cert.pem",
		MTLSKeyFile:  "/nonexistent/key.pem",
		MTLSCAFile:   "/nonexistent/ca.pem",
	}
	_, err := cfg.LoadMTLS()
	if err == nil {
		t.Fatal("LoadMTLS accepted nonexistent files")
	}
	if !strings.Contains(err.Error(), "cert") {
		t.Errorf("err = %q, want it to mention 'cert'", err)
	}
}

func TestLoadMTLSEmptyCertField(t *testing.T) {
	cfg := &Config{URL: "https://coord:8443"}
	_, err := cfg.LoadMTLS()
	if err == nil {
		t.Fatal("LoadMTLS accepted empty cert paths")
	}
}
