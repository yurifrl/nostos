package osimage

import (
	"context"
	"errors"
	"testing"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/paths"
)

// fakeImage is a minimal OSImage for registry tests (no I/O).
type fakeImage struct{ name string }

func (f *fakeImage) Name() string { return f.name }
func (f *fakeImage) Resolve(context.Context, string) (Ref, error) {
	return Ref{OS: f.name}, nil
}
func (f *fakeImage) NodeConfig(context.Context, string, bool) ([]byte, error)   { return nil, nil }
func (f *fakeImage) NetbootScript(context.Context, string, Ref) (string, error) { return "", nil }
func (f *fakeImage) FlashPlan(context.Context, string, Ref) (FlashPlan, error)  { return FlashPlan{}, nil }

func deps(nodes map[string]config.Node) Deps {
	return Deps{Cfg: &config.Config{Nodes: nodes}, Paths: paths.Paths{}}
}

func TestForDefaultsToTalos(t *testing.T) {
	Register("talos-test-default", func(Deps) OSImage { return &fakeImage{name: "talos"} })
	// A node with no boot.pxe block → nodeOS == "talos".
	d := deps(map[string]config.Node{"n1": {}})
	// Register the real default name "talos" only if not present; use a node
	// whose PXETarget() == "talos" and assert New("talos") path via nodeOS.
	if got := nodeOS(d.Cfg.Nodes["n1"]); got != "talos" {
		t.Fatalf("nodeOS default = %q; want talos", got)
	}
}

func TestRegisterAndNew(t *testing.T) {
	Register("fake-os-a", func(Deps) OSImage { return &fakeImage{name: "fake-os-a"} })
	img, err := New("fake-os-a", deps(nil))
	if err != nil {
		t.Fatal(err)
	}
	if img.Name() != "fake-os-a" {
		t.Fatalf("Name = %q", img.Name())
	}
}

func TestNewUnknownRejected(t *testing.T) {
	if _, err := New("nonexistent-os", deps(nil)); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("want ErrNotRegistered, got %v", err)
	}
}

func TestForSelectsByNodeOS(t *testing.T) {
	Register("fake-os-b", func(Deps) OSImage { return &fakeImage{name: "fake-os-b"} })
	// Node whose OS resolves to fake-os-b via the os: block.
	node := config.Node{OS: &config.OSConfig{Name: "fake-os-b"}}
	img, err := For(deps(map[string]config.Node{"n": node}), "n")
	if err != nil {
		t.Fatal(err)
	}
	if img.Name() != "fake-os-b" {
		t.Fatalf("For selected %q; want fake-os-b", img.Name())
	}
}

func TestForUnknownNode(t *testing.T) {
	if _, err := For(deps(map[string]config.Node{}), "missing"); err == nil {
		t.Fatal("want error for missing node")
	}
}

func TestFlashPlanHelpers(t *testing.T) {
	fp := FlashPlan{Parts: []FilePart{
		{Name: "img", Path: "/a", IsMainImage: true},
		{Name: "cfg", Path: "/b"},
	}}
	main, ok := fp.MainImage()
	if !ok || main.Path != "/a" {
		t.Fatalf("MainImage = %+v ok=%v", main, ok)
	}
	if sc := fp.Sidecars(); len(sc) != 1 || sc[0].Path != "/b" {
		t.Fatalf("Sidecars = %+v", sc)
	}
}
