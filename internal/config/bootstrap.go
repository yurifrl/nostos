package config

// Bootstrap is the optional cluster-bootstrap tier. When present, `nostos
// render` synthesizes three inline manifests — the root op-credentials Secret,
// the nostos-bootstrap-config ConfigMap, and the controller bundle — and
// appends them to cluster.inlineManifests of the controlplane machineconfig.
// When absent (nil), render behaves exactly as before: templates own their own
// inline manifests and render synthesizes nothing.
//
// The serialized non-secret block (Cilium/Argocd/Repos/Namespaces) is read by
// the in-cluster nostos-bootstrap controller (see internal/bootstrap.Config),
// so the yaml keys here MUST stay in sync with that struct.
type Bootstrap struct {
	// Cilium is the CNI tier the controller installs first.
	Cilium BootstrapCilium `yaml:"cilium" validate:"required"`
	// Argocd is the GitOps engine the controller installs after ESO is valid.
	Argocd BootstrapArgocd `yaml:"argocd" validate:"required"`
	// Repos are the user GitOps repos; the controller generates one root ArgoCD
	// Application per entry (app-of-apps). At least one is required.
	Repos []BootstrapRepo `yaml:"repos" validate:"required,min=1,dive"`
	// Namespaces are created by the controller before ESO/ArgoCD (replaces the
	// hand-written namespace inline blobs). Order-independent.
	Namespaces []string `yaml:"namespaces,omitempty" validate:"dive,hostname_rfc1123"`
	// ControllerImage pins the nostos-bootstrap controller image. The Deployment
	// emitted by render references repo:tag. The tag MUST already exist in the
	// registry (as a multi-arch manifest list) before any cluster boots with
	// this config, or the controller pod ImagePullBackOffs pre-CNI.
	ControllerImage BootstrapImage `yaml:"controller_image" validate:"required"`
	// RootSecret describes the irreducible 1Password Connect Secret that render
	// emits with op:// refs intact (resolved by secrets.ResolveTemplate).
	RootSecret BootstrapRootSecret `yaml:"root_secret" validate:"required"`
}

// BootstrapCilium pins the Cilium version and optional values that the
// controller renders into embedded manifests (no Helm at runtime). Values is
// an opaque passthrough map nostos does not interpret.
type BootstrapCilium struct {
	Version string         `yaml:"version" validate:"required"`
	Values  map[string]any `yaml:"values,omitempty"`
}

// BootstrapArgocd pins the ArgoCD version the controller installs.
type BootstrapArgocd struct {
	Version string `yaml:"version" validate:"required"`
}

// BootstrapRepo is one user GitOps repo the controller turns into a root app.
type BootstrapRepo struct {
	URL      string `yaml:"url"      validate:"required,url"`
	Path     string `yaml:"path"     validate:"required"`
	Revision string `yaml:"revision,omitempty"`
}

// BootstrapImage is a repo + tag pair for an OCI image.
type BootstrapImage struct {
	Repo string `yaml:"repo" validate:"required"`
	Tag  string `yaml:"tag"  validate:"required"`
}

// Ref returns the full repo:tag image reference.
func (i BootstrapImage) Ref() string { return i.Repo + ":" + i.Tag }

// BootstrapRootSecret describes the one irreducible Secret. Keys map to Secret
// data keys; values are op:// (or sops://, file://) refs left intact through
// the text/template pass and resolved by secrets.ResolveTemplate. Values must
// already be base64-encoded in the backend, because they land in Secret.data
// (NOT stringData) — matching today's secret-op-credentials blob exactly.
type BootstrapRootSecret struct {
	Name      string         `yaml:"name"      validate:"required"`
	Namespace string         `yaml:"namespace" validate:"required"`
	Data      map[string]Ref `yaml:"data"      validate:"required,min=1"`
}
