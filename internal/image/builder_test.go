package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yurifrl/nostos/internal/osimage"
)

// writePlan builds a FlashPlan with a main image file (raw bytes) and optional
// sidecar files, returning the plan.
func writePlan(t *testing.T, mainBytes []byte, sidecars map[string][]byte) osimage.FlashPlan {
	t.Helper()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.raw")
	if err := os.WriteFile(mainPath, mainBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := osimage.FlashPlan{Parts: []osimage.FilePart{
		{Name: "main.raw", Path: mainPath, IsMainImage: true},
	}}
	for name, b := range sidecars {
		sp := filepath.Join(dir, "sc-"+name)
		if err := os.WriteFile(sp, b, 0o644); err != nil {
			t.Fatal(err)
		}
		plan.Parts = append(plan.Parts, osimage.FilePart{Name: name, Path: sp})
	}
	return plan
}

func TestWriteRejectsNoMainImage(t *testing.T) {
	_, err := Write(context.Background(), osimage.FlashPlan{}, Dest{Mode: ModeFile, Path: "/tmp/x"})
	if err == nil {
		t.Fatal("want error for plan with no main image")
	}
}

func TestWriteRejectsCompressedDevice(t *testing.T) {
	plan := writePlan(t, []byte("data"), nil)
	_, err := Write(context.Background(), plan, Dest{Mode: ModeDevice, Path: "/dev/null", Compress: true})
	if err == nil {
		t.Fatal("want error: compress incompatible with device")
	}
}

func TestWriteFileMainImage(t *testing.T) {
	want := []byte("RAW-IMAGE-BYTES")
	plan := writePlan(t, want, nil)
	out := filepath.Join(t.TempDir(), "disk.raw")

	res, err := Write(context.Background(), plan, Dest{Mode: ModeFile, Path: out})
	if err != nil {
		t.Fatal(err)
	}
	if res.ImagePath != out {
		t.Fatalf("ImagePath = %q; want %q", res.ImagePath, out)
	}
	got, _ := os.ReadFile(out)
	if string(got) != string(want) {
		t.Fatalf("written bytes = %q; want %q", got, want)
	}
	if res.BytesOut != int64(len(want)) {
		t.Fatalf("BytesOut = %d; want %d", res.BytesOut, len(want))
	}
}

func TestWriteFileSidecars(t *testing.T) {
	plan := writePlan(t, []byte("img"), map[string][]byte{
		"-config.yaml": []byte("machine: {}"),
	})
	out := filepath.Join(t.TempDir(), "disk.raw")

	res, err := Write(context.Background(), plan, Dest{Mode: ModeFile, Path: out})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sidecars) != 1 {
		t.Fatalf("Sidecars = %v; want 1", res.Sidecars)
	}
	// Sidecar dropped beside the output via sidecarPath: disk-config.yaml.
	wantPath := filepath.Join(filepath.Dir(out), "disk-config.yaml")
	if res.Sidecars[0] != wantPath {
		t.Fatalf("sidecar path = %q; want %q", res.Sidecars[0], wantPath)
	}
	b, _ := os.ReadFile(wantPath)
	if string(b) != "machine: {}" {
		t.Fatalf("sidecar content = %q", b)
	}
}

func TestWriteDeviceSkipsSidecars(t *testing.T) {
	plan := writePlan(t, []byte("img"), map[string][]byte{"-config.yaml": []byte("x")})
	dev := filepath.Join(t.TempDir(), "fakedev") // a regular file standing in for a device
	if err := os.WriteFile(dev, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Write(context.Background(), plan, Dest{Mode: ModeDevice, Path: dev})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sidecars) != 0 {
		t.Fatalf("device mode should write no sidecars, got %v", res.Sidecars)
	}
}

func TestSidecarPath(t *testing.T) {
	cases := []struct{ in, suffix, want string }{
		{"/tmp/edge1.raw", "-config.yaml", "/tmp/edge1-config.yaml"},
		{"/tmp/edge1.raw.xz", "-config.yaml", "/tmp/edge1-config.yaml"},
		{"/tmp/edge1.img", "-eeprom", "/tmp/edge1-eeprom"},
		{"/tmp/edge1", "-config.yaml", "/tmp/edge1-config.yaml"},
	}
	for _, c := range cases {
		if got := sidecarPath(c.in, c.suffix); got != c.want {
			t.Errorf("sidecarPath(%q,%q) = %q; want %q", c.in, c.suffix, got, c.want)
		}
	}
}

func TestWriteEEPROMImageDir(t *testing.T) {
	fw := t.TempDir()
	for _, f := range EEPROMFiles {
		if f == "boot.conf" {
			continue
		}
		if err := os.WriteFile(filepath.Join(fw, f), []byte("fw"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "eeprom")
	if err := WriteEEPROMImage(out, fw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "boot.conf")); err != nil {
		t.Fatalf("boot.conf not written: %v", err)
	}
}
