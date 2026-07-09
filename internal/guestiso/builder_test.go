package guestiso

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/execx/execxtest"
)

func testImage() config.Image {
	return config.Image{
		Build: config.ImageBuild{
			UUPID:        "uup-123",
			Edition:      "professional",
			DriverSource: "https://example.com/virtio-win.iso",
			AnswerFile:   "files/autounattend.xml",
		},
		Store: config.ImageStore{
			Bucket: "op://vault/iso/bucket",
			Object: "Win_combined.iso",
		},
		CredentialsRef: "op://vault/item/creds",
	}
}

func TestBuildConstructsContainerCommandFromConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "files/autounattend.xml"), []byte("<x/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	fc := execxtest.New(execxtest.Script{})
	b := &Builder{Cmd: fc}

	got, err := b.Build(context.Background(), testImage(), root, out)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(out, "Win_combined.iso") {
		t.Errorf("out path = %q", got)
	}
	if len(fc.Calls) != 1 {
		t.Fatalf("calls=%d", len(fc.Calls))
	}
	c := fc.Calls[0]
	if c.Name != "docker" {
		t.Errorf("runtime=%q", c.Name)
	}
	joined := strings.Join(c.Args, " ")
	for _, want := range []string{
		"run --rm --privileged",
		"UUP_ID=uup-123",
		"EDITION=professional",
		"DRIVER_SOURCE=https://example.com/virtio-win.iso",
		"OUT_NAME=Win_combined.iso",
		"debian:13 bash /build.sh",
		"/ctx/autounattend.xml:ro",
		out + ":/out",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\n got: %s", want, joined)
		}
	}
	// No machine identity should leak as a literal beyond what config provided.
	if strings.Contains(joined, "pc01") || strings.Contains(joined, "syscd-") {
		t.Errorf("unexpected hardcoded identity in args: %s", joined)
	}
}

func TestBuildMissingAnswerFileErrors(t *testing.T) {
	fc := execxtest.New(execxtest.Script{})
	b := &Builder{Cmd: fc}
	if _, err := b.Build(context.Background(), testImage(), t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected error for missing answer file")
	}
}
