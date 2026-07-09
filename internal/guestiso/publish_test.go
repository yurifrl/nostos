package guestiso

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/api/option"
)

// fakeGCS returns a minimal server that accepts a single-shot upload and
// reports the object back, enough to exercise Publish without real GCS.
func fakeGCS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body (upload payload) and return an object resource.
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"name":"Win_combined.iso","bucket":"example-bucket"}`))
	}))
}

func TestPublishUploadsToConfiguredTarget(t *testing.T) {
	srv := fakeGCS(t)
	defer srv.Close()

	iso := filepath.Join(t.TempDir(), "Win_combined.iso")
	if err := os.WriteFile(iso, []byte("ISO-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	uri, err := publishWith(context.Background(), "example-bucket", "Win_combined.iso", iso,
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if uri != "gs://example-bucket/Win_combined.iso" {
		t.Errorf("uri = %q", uri)
	}
}

func TestPublishMissingFileErrors(t *testing.T) {
	_, err := Publish(context.Background(), "example-bucket", "Win_combined.iso", "/no/such.iso", fakeSAKey(t))
	if err == nil || !strings.Contains(err.Error(), "open iso") {
		t.Fatalf("expected open error, got %v", err)
	}
}
