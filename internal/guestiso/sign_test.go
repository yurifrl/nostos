package guestiso

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func fakeSAKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	b, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "iso-signer@example-project.iam.gserviceaccount.com",
		"private_key":  string(pemKey),
	})
	return b
}

func TestSignURLProducesV4SignedURL(t *testing.T) {
	url, err := SignURL("example-bucket", "Win_combined.iso", fakeSAKey(t), 6*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"https://storage.googleapis.com/example-bucket/Win_combined.iso",
		"X-Goog-Algorithm=GOOG4-RSA-SHA256",
		"X-Goog-Signature=",
		"X-Goog-Credential=",
	} {
		if !strings.Contains(url, want) {
			t.Errorf("signed url missing %q\n got: %s", want, url)
		}
	}
}

func TestSignURLClampsTTL(t *testing.T) {
	// Over-long and non-positive both clamp to max (no error).
	for _, d := range []time.Duration{0, -time.Hour, 100 * 24 * time.Hour} {
		if _, err := SignURL("example-bucket", "o.iso", fakeSAKey(t), d); err != nil {
			t.Errorf("d=%v: %v", d, err)
		}
	}
}

func TestSignURLBadKey(t *testing.T) {
	if _, err := SignURL("example-bucket", "o.iso", []byte(`{"client_email":"x"}`), time.Hour); err == nil {
		t.Fatal("expected error for key missing private_key")
	}
}

func TestPasteSnippet(t *testing.T) {
	s := PasteSnippet("https://signed")
	if !strings.Contains(s, "url: \"https://signed\"") || !strings.Contains(s, "isos:") {
		t.Errorf("snippet = %q", s)
	}
}
