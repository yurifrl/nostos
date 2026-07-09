package proxmox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseIndexFindsAllReleases(t *testing.T) {
	versions := ParseIndex(readFixture(t, "index.html"))
	// 7.4-1, 8.2-2, 8.9-1, 8.10-1, 8.3-1 = 5 unique releases.
	if len(versions) != 5 {
		t.Fatalf("ParseIndex found %d releases; want 5: %+v", len(versions), versions)
	}
}

func TestSelectLatestNumericOrdering(t *testing.T) {
	versions := ParseIndex(readFixture(t, "index.html"))
	latest, err := SelectLatest(versions)
	if err != nil {
		t.Fatal(err)
	}
	// Numeric tuple ordering: 8.10-1 must beat 8.9-1 (lexical would pick 8.9).
	if latest.String() != "8.10-1" {
		t.Fatalf("SelectLatest = %s; want 8.10-1 (numeric, not lexical)", latest.String())
	}
}

func TestSelectLatestEmpty(t *testing.T) {
	if _, err := SelectLatest(nil); !errors.Is(err, ErrNoReleases) {
		t.Fatalf("want ErrNoReleases, got %v", err)
	}
}

func TestFindPinnedHit(t *testing.T) {
	versions := ParseIndex(readFixture(t, "index.html"))
	v, err := FindPinned(versions, "8.3-1")
	if err != nil {
		t.Fatal(err)
	}
	if v.File != "proxmox-ve_8.3-1.iso" {
		t.Fatalf("File = %q; want proxmox-ve_8.3-1.iso", v.File)
	}
}

func TestFindPinnedMiss(t *testing.T) {
	versions := ParseIndex(readFixture(t, "index.html"))
	if _, err := FindPinned(versions, "9.9-9"); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("want ErrVersionNotFound, got %v", err)
	}
}

func TestParseChecksums(t *testing.T) {
	body := "abc123  proxmox-ve_8.10-1.iso\ndeadbeef  other.iso\n"
	if got := ParseChecksums(body, "proxmox-ve_8.10-1.iso"); got != "abc123" {
		t.Fatalf("ParseChecksums = %q; want abc123", got)
	}
	if got := ParseChecksums(body, "missing.iso"); got != "" {
		t.Fatalf("ParseChecksums(missing) = %q; want empty", got)
	}
}

// newFixtureServer serves the index fixture plus a SHA256SUMS so Resolve can be
// exercised end-to-end with NO live network.
func newFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	index := readFixture(t, "index.html")
	mux := http.NewServeMux()
	mux.HandleFunc("/iso/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/iso/SHA256SUMS":
			_, _ = w.Write([]byte("feedface  proxmox-ve_8.10-1.iso\n"))
		default:
			_, _ = w.Write([]byte(index))
		}
	})
	return httptest.NewServer(mux)
}

func TestResolveLatestAgainstFixtureServer(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()
	r := &Resolver{IndexURL: srv.URL + "/iso/"}

	spec, err := r.Resolve(context.Background(), "latest")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "8.10-1" {
		t.Fatalf("Version = %q; want 8.10-1", spec.Version)
	}
	if spec.ISOURL != srv.URL+"/iso/proxmox-ve_8.10-1.iso" {
		t.Fatalf("ISOURL = %q", spec.ISOURL)
	}
	if spec.SHA256 != "feedface" {
		t.Fatalf("SHA256 = %q; want feedface (recorded for auditability)", spec.SHA256)
	}
}

func TestResolvePinnedAgainstFixtureServer(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()
	r := &Resolver{IndexURL: srv.URL + "/iso/"}

	spec, err := r.Resolve(context.Background(), "8.3-1")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Version != "8.3-1" {
		t.Fatalf("Version = %q; want 8.3-1", spec.Version)
	}
}

func TestResolvePinnedMissAgainstFixtureServer(t *testing.T) {
	srv := newFixtureServer(t)
	defer srv.Close()
	r := &Resolver{IndexURL: srv.URL + "/iso/"}

	if _, err := r.Resolve(context.Background(), "9.9-9"); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("want ErrVersionNotFound, got %v", err)
	}
}
