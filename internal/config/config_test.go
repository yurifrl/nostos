package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
cluster:
  name: talos-default
  endpoint: https://10.0.0.10:6443
  talos_version: v1.10.3
  schematic_id: 4a0d65c669d46663f377e7161e50cfd570c401f26fd9e7bda34a0216b6f1922b
secrets:
  backend: onepassword
  onepassword:
    account: my.1password.com
    vault: my-vault
nodes:
  cp1:
    mac: "d0:94:66:d9:eb:a5"
    ip: 10.0.0.10
    role: controlplane
    arch: amd64
    install_disk: /dev/nvme0n1
    template: cp1.yaml
`

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeYAML(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cluster.Name != "talos-default" {
		t.Errorf("cluster.name = %q", cfg.Cluster.Name)
	}
	n, ok := cfg.Nodes["cp1"]
	if !ok {
		t.Fatal("cp1 missing")
	}
	if n.MAC != "d0:94:66:d9:eb:a5" {
		t.Errorf("MAC = %q", n.MAC)
	}
	if n.MACHyphen() != "d0-94-66-d9-eb-a5" {
		t.Errorf("MACHyphen = %q", n.MACHyphen())
	}
}

func TestUppercaseMACNormalized(t *testing.T) {
	body := strings.Replace(validYAML, "d0:94:66:d9:eb:a5", "D0:94:66:D9:EB:A5", 1)
	cfg, err := Load(writeYAML(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Nodes["cp1"].MAC != "d0:94:66:d9:eb:a5" {
		t.Errorf("MAC = %q; want lowercase", cfg.Nodes["cp1"].MAC)
	}
}

func TestInvalidMAC(t *testing.T) {
	body := strings.Replace(validYAML, `"d0:94:66:d9:eb:a5"`, `"not-a-mac"`, 1)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "MAC") {
		t.Fatalf("want MAC error, got %v", err)
	}
}

func TestDuplicateMAC(t *testing.T) {
	body := validYAML + `
  cp2:
    mac: "d0:94:66:d9:eb:a5"
    ip: 10.0.0.11
    role: worker
    arch: amd64
    install_disk: /dev/nvme0n1
    template: cp2.yaml
`
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "duplicate MAC") {
		t.Fatalf("want duplicate MAC error, got %v", err)
	}
	if !strings.Contains(err.Error(), "cp1") || !strings.Contains(err.Error(), "cp2") {
		t.Errorf("err missing node names: %v", err)
	}
}

func TestMissingRequired(t *testing.T) {
	body := strings.Replace(validYAML, "name: talos-default\n", "", 1)
	_, err := Load(writeYAML(t, body))
	if err == nil {
		t.Fatal("expected validation failure")
	}
}

func TestOnepasswordWithoutBlock(t *testing.T) {
	body := strings.Replace(
		validYAML,
		"backend: onepassword\n  onepassword:\n    account: my.1password.com\n    vault: my-vault\n",
		"backend: onepassword\n",
		1,
	)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "onepassword block") {
		t.Fatalf("want onepassword block error, got %v", err)
	}
}

func TestInvalidNodeName(t *testing.T) {
	body := strings.Replace(validYAML, "cp1:", "Cp1!:", 1)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "invalid node name") {
		t.Fatalf("want invalid node name error, got %v", err)
	}
}

func TestHTTPEndpointRejected(t *testing.T) {
	body := strings.Replace(validYAML, "https://10.0.0.10", "http://10.0.0.10", 1)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("want https rule error, got %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmptyFile(t *testing.T) {
	_, err := Load(writeYAML(t, ""))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty error, got %v", err)
	}
}

const validImageYAML = validYAML + `
images:
  windows:
    build:
      uup_id: f7e8991e-4fd8-4bfd-a404-0de6dccd4191
      edition: professional
      driver_source: https://example.com/virtio-win.iso
      answer_file: files/autounattend.xml
    store:
      bucket: op://kubernetes/iso/bucket
      object: Win11_combined.iso
    credentials_ref: op://kubernetes/crossplane-gcp/creds
`

func TestImageLoadAndLookup(t *testing.T) {
	cfg, err := Load(writeYAML(t, validImageYAML))
	if err != nil {
		t.Fatal(err)
	}
	img, err := cfg.ImageByName("windows")
	if err != nil {
		t.Fatal(err)
	}
	if img.Build.UUPID == "" || img.Store.Bucket.String() != "op://kubernetes/iso/bucket" {
		t.Errorf("image not parsed: %+v", img)
	}
	if string(img.CredentialsRef) != "op://kubernetes/crossplane-gcp/creds" {
		t.Errorf("credentials_ref = %q", img.CredentialsRef)
	}
}

func TestImageMissingLookup(t *testing.T) {
	cfg, err := Load(writeYAML(t, validImageYAML))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.ImageByName("nope"); err == nil {
		t.Fatal("expected error for unknown image name")
	}
}

func TestInvalidImageName(t *testing.T) {
	body := strings.Replace(validImageYAML, "  windows:", "  Windows_X:", 1)
	if _, err := Load(writeYAML(t, body)); err == nil || !strings.Contains(err.Error(), "image name") {
		t.Fatalf("expected image name error, got %v", err)
	}
}

func TestImageMissingRequiredField(t *testing.T) {
	body := strings.Replace(validImageYAML, "      bucket: op://kubernetes/iso/bucket\n", "", 1)
	if _, err := Load(writeYAML(t, body)); err == nil {
		t.Fatal("expected error for missing store.bucket")
	}
}
