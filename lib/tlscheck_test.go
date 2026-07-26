package lib

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadACMECertificate(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "app.example.com"},
		DNSNames:     []string{"app.example.com"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(60 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	document := map[string]any{
		"cloudflare": map[string]any{
			"Certificates": []any{map[string]any{
				"domain":      map[string]any{"main": "app.example.com"},
				"certificate": encoded,
			}},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	acmeDir := filepath.Join(dataDir, "traefik")
	if err := os.MkdirAll(acmeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acmeDir, "acme.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ReadACMECertificate(dataDir, "app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if delta := info.NotAfter.Sub(template.NotAfter); delta < -time.Second || delta > time.Second {
		t.Fatalf("unexpected expiry %s", info.NotAfter)
	}
}
