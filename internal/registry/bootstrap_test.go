package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/paths"
)

// cluster: block is required for injection; keep a second doc to prove
// multi-document templates are re-joined intact.
const bootstrapTmplBody = `machine:
  type: controlplane
  install:
    image: {{ .InstallImage }}
cluster:
  clusterName: talos-default
  network:
    cni:
      name: none
---
apiVersion: v1alpha1
kind: ExtensionServiceConfig
name: tailscale
environment:
  - TS_AUTHKEY=stub
`

// writeBootstrapEnv writes a config.yaml + templates and returns the temp dir.
// Root-secret data uses file:// refs (always-registered backend) so the test
// resolves without 1Password — exercising the same ResolveTemplate pass op://
// would use.
func writeBootstrapEnv(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()

	credFile := filepath.Join(dir, "creds")
	tokFile := filepath.Join(dir, "tok")
	if err := os.WriteFile(credFile, []byte("BASE64CREDS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokFile, []byte("BASE64TOKEN"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgYAML := `
cluster:
  name: talos-default
  endpoint: https://10.0.0.10:6443
  talos_version: v1.10.3
  schematic_id: 4a0d65c669d46663f377e7161e50cfd570c401f26fd9e7bda34a0216b6f1922b
secrets:
  backend: env
bootstrap:
  cilium:
    version: v1.18.0
    values:
      kubeProxyReplacement: false
  argocd:
    version: v3.4.3
  repos:
    - url: https://github.com/yurifrl/home-systems.git
      path: k8s/applications
      revision: main
  namespaces: [argocd, external-secrets, 1password]
  controller_image:
    repo: ghcr.io/yurifrl/nostos-bootstrap
    tag: bootstrap-v0.1.0
  root_secret:
    name: op-credentials
    namespace: 1password
    data:
      1password-credentials.json: file://` + credFile + `
      token: file://` + tokFile + `
nodes:
  cp1:
    mac: d0:94:66:d9:eb:a5
    ip: 10.0.0.101
    role: controlplane
    arch: amd64
    install_disk: /dev/nvme0n1
    template: cp1.yaml
  w1:
    mac: d0:94:66:d9:eb:a6
    ip: 10.0.0.107
    role: worker
    arch: amd64
    install_disk: /dev/sda
    template: w1.yaml
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"cp1.yaml", "w1.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, "templates", f), []byte(bootstrapTmplBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, cfgPath
}

// TestRenderInjectsBootstrapManifests verifies the three synthesized inline
// manifests land on a controlplane node with file:// refs resolved, values
// under data: (not stringData), and the second YAML document preserved.
func TestRenderInjectsBootstrapManifests(t *testing.T) {
	_, cfgPath := writeBootstrapEnv(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := paths.New(cfgPath)

	out, err := Render(cfg, p, "cp1", false)
	if err != nil {
		t.Fatalf("Render(cp1): %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)

	for _, want := range []string{
		"secret-op-credentials",
		"nostos-bootstrap-config",
		"nostos-bootstrap-controller",
		"ghcr.io/yurifrl/nostos-bootstrap:bootstrap-v0.1.0", // controller image
		"version: v1.18.0",                                  // cilium version in ConfigMap
		"kind: ClusterRole",                                 // controller RBAC
		"BASE64CREDS",                                       // file:// resolved
		"BASE64TOKEN",
		"kind: ExtensionServiceConfig", // second doc preserved
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\n---\n%s", want, got)
		}
	}
	// Secret values under data: (base64), never stringData.
	if strings.Contains(got, "stringData") {
		t.Errorf("rendered output uses stringData; must use data:\n%s", got)
	}
	// file:// refs must be fully resolved (none left literal).
	if strings.Contains(got, "file://") {
		t.Errorf("unresolved file:// ref remains in output:\n%s", got)
	}
}

// TestRenderWorkerSkipsBootstrap proves bootstrap manifests are only injected
// into controlplane nodes.
func TestRenderWorkerSkipsBootstrap(t *testing.T) {
	_, cfgPath := writeBootstrapEnv(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := paths.New(cfgPath)

	out, err := Render(cfg, p, "w1", false)
	if err != nil {
		t.Fatalf("Render(w1): %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "nostos-bootstrap-controller") {
		t.Errorf("worker node got bootstrap manifests:\n%s", string(data))
	}
}

// TestRenderBootstrapIdempotent pins the idempotency guarantee: re-rendering is
// byte-identical.
func TestRenderBootstrapIdempotent(t *testing.T) {
	_, cfgPath := writeBootstrapEnv(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := paths.New(cfgPath)

	out1, err := Render(cfg, p, "cp1", false)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(out1)
	out2, err := Render(cfg, p, "cp1", false)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(out2)
	if string(first) != string(second) {
		t.Errorf("render not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestRenderNilBootstrapUnchanged proves nil bootstrap = legacy behavior: the
// template body is untouched apart from the install-image substitution.
func TestRenderNilBootstrapUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := `
cluster:
  name: talos-default
  endpoint: https://10.0.0.10:6443
  talos_version: v1.10.3
  schematic_id: 4a0d65c669d46663f377e7161e50cfd570c401f26fd9e7bda34a0216b6f1922b
secrets:
  backend: env
nodes:
  cp1:
    mac: d0:94:66:d9:eb:a5
    ip: 10.0.0.101
    role: controlplane
    arch: amd64
    install_disk: /dev/nvme0n1
    template: cp1.yaml
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "cp1.yaml"), []byte(bootstrapTmplBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := paths.New(cfgPath)
	out, err := Render(cfg, p, "cp1", false)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if strings.Contains(string(data), "nostos-bootstrap") {
		t.Errorf("nil bootstrap should not synthesize manifests:\n%s", string(data))
	}
}

// TestRenderDoubleEmissionGuard verifies render fails loud when a template
// still hand-writes inlineManifests while a bootstrap: block is configured.
func TestRenderDoubleEmissionGuard(t *testing.T) {
	dir, cfgPath := writeBootstrapEnv(t)
	// Overwrite cp1.yaml with a template that hand-writes inlineManifests.
	bad := `machine:
  type: controlplane
  install:
    image: {{ .InstallImage }}
cluster:
  clusterName: talos-default
  inlineManifests:
    - name: namespace-argocd
      contents: |
        apiVersion: v1
        kind: Namespace
        metadata:
          name: argocd
`
	if err := os.WriteFile(filepath.Join(dir, "templates", "cp1.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p := paths.New(cfgPath)
	_, err = Render(cfg, p, "cp1", false)
	if err == nil || !strings.Contains(err.Error(), "hand-writes cluster.inlineManifests") {
		t.Fatalf("want double-emission guard error, got %v", err)
	}
}
