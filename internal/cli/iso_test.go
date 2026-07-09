package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeISOConfig(t *testing.T) string {
	t.Helper()
	body := `cluster:
  name: test
  endpoint: https://192.168.99.99:6443
  talos_version: v1.10.3
  schematic_id: 6e4e8b75e7c1cf2c3f1cf2c3f1cf2c3f1cf2c3f1cf2c3f1cf2c3f1cf2c3f1cf2
secrets:
  backend: onepassword
  onepassword:
    account: foo.1password.com
    vault: my-vault
images:
  win:
    build:
      uup_id: uup-123
      edition: professional
      driver_source: https://example.com/virtio-win.iso
      answer_file: files/autounattend.xml
    store:
      bucket: op://my-vault/iso/bucket
      object: Win_combined.iso
    credentials_ref: op://my-vault/gcp/creds
`
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestISOHelpListsSubcommands(t *testing.T) {
	stdout, _, err := run(t, "iso", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"build", "publish", "url", "prepare"} {
		if !strings.Contains(stdout, sub) {
			t.Errorf("iso help missing subcommand %q\n%s", sub, stdout)
		}
	}
}

func TestISOBuildRequiresName(t *testing.T) {
	if _, _, err := run(t, "iso", "build"); err == nil {
		t.Fatal("expected error when NAME omitted")
	}
}

func TestISOURLUnknownImage(t *testing.T) {
	cfg := writeISOConfig(t)
	_, _, err := run(t, "--config", cfg, "iso", "url", "no-such")
	if err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("expected unknown-image error, got %v", err)
	}
}
