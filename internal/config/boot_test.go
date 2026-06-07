package config

import (
	"strings"
	"testing"
)

const cp1Only = `
cluster:
  name: talos-default
  endpoint: https://10.0.0.10:6443
  talos_version: v1.10.3
  schematic_id: 4a0d65c669d46663f377e7161e50cfd570c401f26fd9e7bda34a0216b6f1922b
secrets:
  backend: env
nodes:
  cp1:
    mac: "d0:94:66:d9:eb:a5"
    ip: 10.0.0.10
    role: controlplane
    arch: amd64
    install_disk: /dev/nvme0n1
    template: cp1.yaml
`

const w1w2 = `
cluster:
  name: talos-default
  endpoint: https://10.0.0.10:6443
  talos_version: v1.10.3
  schematic_id: 4a0d65c669d46663f377e7161e50cfd570c401f26fd9e7bda34a0216b6f1922b
secrets:
  backend: env
nodes:
  w1:
    mac: "02:00:00:00:00:01"
    ip: 10.0.0.11
    role: worker
    arch: arm64
    install_disk: /dev/mmcblk0
    template: tp.yaml
    boot:
      method: tpi
      tpi:
        host: "10.0.0.2"
        slot: 1
        username_ref: "op://my-vault/turingpi/username"
        password_ref: "op://my-vault/turingpi/password"
  w2:
    mac: "02:00:00:00:00:04"
    ip: 10.0.0.14
    role: worker
    arch: arm64
    install_disk: /dev/mmcblk0
    template: tp.yaml
    boot:
      method: tpi
      tpi:
        host: "10.0.0.2"
        slot: 4
        identity_file_ref: "op://my-vault/turingpi/ssh_key"
`

func TestLoadCp1OnlyDefaultsToPXE(t *testing.T) {
	cfg, err := Load(writeYAML(t, cp1Only))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Nodes["cp1"]
	if n.Boot.Method != "pxe" {
		t.Fatalf("Boot.Method = %q; want pxe", n.Boot.Method)
	}
	if n.Boot.TPI != nil {
		t.Fatalf("Boot.TPI = %+v; want nil", n.Boot.TPI)
	}
}

func TestLoadW1W2(t *testing.T) {
	cfg, err := Load(writeYAML(t, w1w2))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Nodes["w1"].Boot.Method; got != "tpi" {
		t.Fatalf("w1 method = %q", got)
	}
	if got := cfg.Nodes["w1"].Boot.TPI.Slot; got != 1 {
		t.Fatalf("w1 slot = %d", got)
	}
	if got := cfg.Nodes["w2"].Boot.TPI.IdentityFileRef; got == "" {
		t.Fatal("w2 identity_file_ref empty")
	}
}

func TestDuplicateHostSlotRejected(t *testing.T) {
	body := strings.Replace(w1w2, "slot: 4", "slot: 1", 1)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "duplicate (host, slot)") {
		t.Fatalf("want collision error, got %v", err)
	}
	if !strings.Contains(err.Error(), "w1") || !strings.Contains(err.Error(), "w2") {
		t.Errorf("err missing both names: %v", err)
	}
}

func TestTPINoCredsAllowed(t *testing.T) {
	body := strings.Replace(w1w2,
		`        username_ref: "op://my-vault/turingpi/username"
        password_ref: "op://my-vault/turingpi/password"`,
		"", 1)
	body = strings.Replace(body,
		`        identity_file_ref: "op://my-vault/turingpi/ssh_key"`,
		"", 1)
	cfg, err := Load(writeYAML(t, body))
	if err != nil {
		t.Fatalf("want creds-less tpi to load, got %v", err)
	}
	if cfg.Nodes["w1"].Boot.TPI.UsernameRef != "" {
		t.Fatalf("w1 username_ref leaked: %q", cfg.Nodes["w1"].Boot.TPI.UsernameRef)
	}
	if cfg.Nodes["w2"].Boot.TPI.IdentityFileRef != "" {
		t.Fatalf("w2 identity_file_ref leaked")
	}
}

func TestTPIMACOptional(t *testing.T) {
	body := strings.Replace(w1w2,
		`    mac: "02:00:00:00:00:01"
`, "", 1)
	body = strings.Replace(body,
		`    mac: "02:00:00:00:00:04"
`, "", 1)
	if _, err := Load(writeYAML(t, body)); err != nil {
		t.Fatalf("tpi nodes should not require mac, got %v", err)
	}
}

func TestPXERequiresMAC(t *testing.T) {
	body := strings.Replace(cp1Only,
		`    mac: "d0:94:66:d9:eb:a5"
`, "", 1)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "requires mac") {
		t.Fatalf("want pxe mac requirement error, got %v", err)
	}
}

func TestTPIRequiresCreds(t *testing.T) {
	// With v0.2 relaxation, missing creds is allowed (cached token / prompt).
	body := strings.Replace(w1w2,
		`        username_ref: "op://my-vault/turingpi/username"
        password_ref: "op://my-vault/turingpi/password"`,
		"", 1)
	if _, err := Load(writeYAML(t, body)); err != nil {
		t.Fatalf("creds optional now, got %v", err)
	}
}

func TestRefRejectsEnvScheme(t *testing.T) {
	body := strings.Replace(w1w2,
		`"op://my-vault/turingpi/username"`,
		`"env://TPI_USER"`, 1)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "env://") {
		t.Fatalf("want env:// rejection, got %v", err)
	}
}

func TestRefRejectsInlineLiteral(t *testing.T) {
	body := strings.Replace(w1w2,
		`"op://my-vault/turingpi/password"`,
		`"hunter2"`, 1)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "must start with") {
		t.Fatalf("want literal rejection, got %v", err)
	}
}
