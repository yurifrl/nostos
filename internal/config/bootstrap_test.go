package config

import (
	"strings"
	"testing"
)

const bootstrapYAML = validYAML + `
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
      1password-credentials.json: op://kubernetes/op-credentials/OP_CREDENTIALS_JSON
      token: op://kubernetes/op-credentials/OP_CONNECT_TOKEN
`

func TestLoadBootstrapPresent(t *testing.T) {
	cfg, err := Load(writeYAML(t, bootstrapYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bootstrap == nil {
		t.Fatal("bootstrap block was not parsed")
	}
	b := cfg.Bootstrap
	if b.Cilium.Version != "v1.18.0" {
		t.Errorf("cilium.version = %q", b.Cilium.Version)
	}
	if b.Argocd.Version != "v3.4.3" {
		t.Errorf("argocd.version = %q", b.Argocd.Version)
	}
	if len(b.Repos) != 1 || b.Repos[0].Path != "k8s/applications" {
		t.Errorf("repos = %+v", b.Repos)
	}
	if got := b.ControllerImage.Ref(); got != "ghcr.io/yurifrl/nostos-bootstrap:bootstrap-v0.1.0" {
		t.Errorf("ControllerImage.Ref() = %q", got)
	}
	if b.RootSecret.Name != "op-credentials" || b.RootSecret.Namespace != "1password" {
		t.Errorf("root_secret = %+v", b.RootSecret)
	}
	if len(b.RootSecret.Data) != 2 {
		t.Errorf("root_secret.data len = %d", len(b.RootSecret.Data))
	}
	// op:// refs survive the Ref allowlist.
	if got := b.RootSecret.Data["token"].String(); got != "op://kubernetes/op-credentials/OP_CONNECT_TOKEN" {
		t.Errorf("token ref = %q", got)
	}
}

// TestLoadBootstrapAbsent pins nil = legacy behavior.
func TestLoadBootstrapAbsent(t *testing.T) {
	cfg, err := Load(writeYAML(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bootstrap != nil {
		t.Fatalf("expected nil Bootstrap when block absent, got %+v", cfg.Bootstrap)
	}
}

// TestBootstrapRejectsEnvRef proves the Ref allowlist applies to root_secret
// data values (env:// is rejected for credential refs).
func TestBootstrapRejectsEnvRef(t *testing.T) {
	body := strings.Replace(
		bootstrapYAML,
		"token: op://kubernetes/op-credentials/OP_CONNECT_TOKEN",
		"token: env://OP_CONNECT_TOKEN",
		1,
	)
	_, err := Load(writeYAML(t, body))
	if err == nil || !strings.Contains(err.Error(), "env://") {
		t.Fatalf("want env:// rejection, got %v", err)
	}
}

// TestBootstrapMissingRequiredSubfield fires the dive validator when a
// required sub-field of a present bootstrap block is missing.
func TestBootstrapMissingRequiredSubfield(t *testing.T) {
	body := strings.Replace(bootstrapYAML, "    version: v1.18.0\n", "", 1)
	_, err := Load(writeYAML(t, body))
	if err == nil {
		t.Fatal("expected validation failure for missing cilium.version")
	}
}
