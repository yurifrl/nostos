package proxmox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yurifrl/nostos/internal/config"
	nbproxmox "github.com/yurifrl/nostos/internal/netboot/proxmox"
	"github.com/yurifrl/nostos/internal/osimage"
	"github.com/yurifrl/nostos/internal/paths"
)

// fixtureServer serves a minimal Proxmox index, a SHA256SUMS, and a fake ISO,
// so the proxmox OSImage can be exercised end-to-end with no live network.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	const index = `<a href="proxmox-ve_8.10-1.iso">proxmox-ve_8.10-1.iso</a>`
	const isoBody = "FAKE-PROXMOX-ISO"
	mux := http.NewServeMux()
	mux.HandleFunc("/iso/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/iso/proxmox-ve_8.10-1.iso":
			_, _ = w.Write([]byte(isoBody))
		case "/iso/SHA256SUMS":
			// Omit checksum (404) so DownloadISO skips verification in the test;
			// checksum resolution itself is covered by the resolver's own tests.
			http.NotFound(w, r)
		default:
			_, _ = w.Write([]byte(index))
		}
	})
	return httptest.NewServer(mux)
}

func proxmoxDeps(t *testing.T) osimage.Deps {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg := &config.Config{
		Nodes: map[string]config.Node{
			"pc01": {
				MAC:  "fc:3c:d7:27:66:17",
				IP:   "192.168.68.101",
				Arch: "amd64",
				OS:   &config.OSConfig{Name: "proxmox", Version: "latest"},
			},
		},
	}
	return osimage.Deps{Cfg: cfg, Paths: paths.New("/tmp/nostos-proxmox-test/config.yaml")}
}

func TestProxmoxNodeConfigIsNil(t *testing.T) {
	img := &Image{}
	b, err := img.NodeConfig(context.Background(), "pc01", true)
	if err != nil || b != nil {
		t.Fatalf("NodeConfig = (%v, %v); want (nil, nil)", b, err)
	}
}

func TestProxmoxFlashPlanSingleISO(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()
	orig := nbproxmox.DefaultIndexURL
	nbproxmox.DefaultIndexURL = srv.URL + "/iso/"
	defer func() { nbproxmox.DefaultIndexURL = orig }()

	deps := proxmoxDeps(t)
	if err := deps.Paths.EnsureState(); err != nil {
		t.Fatal(err)
	}
	img := &Image{cfg: deps.Cfg, paths: deps.Paths}

	ref, err := img.Resolve(context.Background(), "pc01")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Version != "8.10-1" {
		t.Fatalf("resolved version = %q; want 8.10-1", ref.Version)
	}

	plan, err := img.FlashPlan(context.Background(), "pc01", ref)
	if err != nil {
		t.Fatal(err)
	}
	// Single ISO part, no sidecars.
	if len(plan.Parts) != 1 {
		t.Fatalf("FlashPlan parts = %d; want 1 (single ISO)", len(plan.Parts))
	}
	main, ok := plan.MainImage()
	if !ok || !main.IsMainImage {
		t.Fatalf("no main image part: %+v", plan.Parts)
	}
	if len(plan.Sidecars()) != 0 {
		t.Fatalf("proxmox flash plan must have no sidecars, got %v", plan.Sidecars())
	}
}
