package guestiso

import (
	"encoding/json"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
)

// MaxSignedURLTTL is the provider cap for V4 signed URLs.
const MaxSignedURLTTL = 7 * 24 * time.Hour

// saKey is the subset of a GCP service-account key JSON used for signing.
type saKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

// SignURL mints a V4 signed GET URL for bucket/object, valid for d (clamped to
// (0, MaxSignedURLTTL]). Signing is local (no network); saKeyJSON is the
// service-account key resolved from the entry's op:// ref.
func SignURL(bucket, object string, saKeyJSON []byte, d time.Duration) (string, error) {
	if d <= 0 || d > MaxSignedURLTTL {
		d = MaxSignedURLTTL
	}
	var k saKey
	if err := json.Unmarshal(saKeyJSON, &k); err != nil {
		return "", fmt.Errorf("parse service-account key: %w", err)
	}
	if k.ClientEmail == "" || k.PrivateKey == "" {
		return "", fmt.Errorf("service-account key missing client_email/private_key")
	}
	url, err := storage.SignedURL(bucket, object, &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		Method:         "GET",
		GoogleAccessID: k.ClientEmail,
		PrivateKey:     []byte(k.PrivateKey),
		Expires:        time.Now().Add(d),
	})
	if err != nil {
		return "", fmt.Errorf("sign url: %w", err)
	}
	return url, nil
}

// PasteSnippet renders the crossplane-proxmox private-values block for a URL.
func PasteSnippet(url string) string {
	return "isos:\n  win11:\n    enabled: true\n    url: \"" + url + "\"\n"
}
