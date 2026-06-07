package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/paths"
)

const baseYAML = `
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

const tmplBody = `machine:
  type: controlplane
  network:
    hostname: cp1
---
apiVersion: v1alpha1
kind: ExtensionServiceConfig
name: tailscale
environment:
  - TS_AUTHKEY=env://NOSTOS_TEST_TS_AUTHKEY
`

func setupConsumer(t *testing.T) (string, *config.Config, paths.Paths) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(baseYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "cp1.yaml"), []byte(tmplBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfgPath, cfg, paths.New(cfgPath)
}

func TestList(t *testing.T) {
	_, cfg, _ := setupConsumer(t)
	list := List(cfg)
	if len(list) != 1 || list[0].Name != "cp1" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestGetUnknown(t *testing.T) {
	_, cfg, _ := setupConsumer(t)
	_, err := Get(cfg, "nope")
	if err == nil || !strings.Contains(err.Error(), "no such node") {
		t.Fatalf("want no such node, got %v", err)
	}
}

func TestAddAndRemove(t *testing.T) {
	cfgPath, _, _ := setupConsumer(t)
	node := config.Node{
		MAC:         "aa:bb:cc:dd:ee:ff",
		IP:          "10.0.0.107",
		Role:        "worker",
		Arch:        "arm64",
		InstallDisk: "/dev/mmcblk0",
		Template:    "w1.yaml",
	}
	if err := Add(cfgPath, "w1", node); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(cfgPath)
	if _, ok := cfg.Nodes["w1"]; !ok {
		t.Fatal("w1 not present after Add")
	}
	if err := Add(cfgPath, "w1", node); err == nil {
		t.Fatal("duplicate Add should fail")
	}
	if err := Remove(cfgPath, "w1"); err != nil {
		t.Fatal(err)
	}
	cfg2, _ := config.Load(cfgPath)
	if _, ok := cfg2.Nodes["w1"]; ok {
		t.Fatal("w1 not removed")
	}
}

func TestRenderProducesMACFilename(t *testing.T) {
	t.Setenv("NOSTOS_TEST_TS_AUTHKEY", "tskey-fake")
	_, cfg, p := setupConsumer(t)
	out, err := Render(cfg, p, "cp1", false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(out) != "d0-94-66-d9-eb-a5.yaml" {
		t.Errorf("filename = %q", filepath.Base(out))
	}
	body, _ := os.ReadFile(out)
	if !strings.Contains(string(body), "TS_AUTHKEY=tskey-fake") {
		t.Errorf("secret not injected: %s", body)
	}
	if strings.Contains(string(body), "env://") {
		t.Errorf("env:// not resolved: %s", body)
	}
}

func TestRenderMissingTemplate(t *testing.T) {
	_, cfg, p := setupConsumer(t)
	if err := os.Remove(filepath.Join(p.Templates(), "cp1.yaml")); err != nil {
		t.Fatal(err)
	}
	_, err := Render(cfg, p, "cp1", false)
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("want template error, got %v", err)
	}
}
