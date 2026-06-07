package bootstrap

import (
	"bytes"
	"embed"
	"text/template"
)

// manifestFS holds the embedded tier manifests and templates. Static tier
// payloads (cilium/eso/argocd) are currently TODO placeholders; the
// ClusterSecretStore and root-app are Go text/templates rendered at runtime.
//
//go:embed manifests/*.yaml manifests/*.tmpl.yaml
var manifestFS embed.FS

// Embedded manifest paths.
const (
	manifestCilium = "manifests/cilium.yaml"
	manifestESO    = "manifests/eso.yaml"
	manifestArgoCD = "manifests/argocd.yaml"

	templateStore   = "manifests/clustersecretstore.tmpl.yaml"
	templateRootApp = "manifests/rootapp.tmpl.yaml"

	// ControllerRBACManifest / ControllerDeploymentManifest are the controller's
	// own in-cluster install YAML, exported so nostos render can inject them
	// into the Talos machineconfig (design §3/§4.3).
	ControllerRBACManifest       = "manifests/controller-rbac.yaml"
	ControllerDeploymentManifest = "manifests/controller-deployment.yaml"
)

// readManifest returns the raw bytes of an embedded manifest.
func readManifest(path string) ([]byte, error) {
	return manifestFS.ReadFile(path)
}

// renderTemplate executes an embedded text/template against data and returns
// the rendered YAML. missingkey=error fails loud on unknown fields.
func renderTemplate(path string, data any) ([]byte, error) {
	raw, err := manifestFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(path).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// storeTemplateData feeds clustersecretstore.tmpl.yaml.
type storeTemplateData struct {
	StoreName       string
	SecretName      string
	SecretNamespace string
}

// appTemplateData feeds rootapp.tmpl.yaml.
type appTemplateData struct {
	Name          string
	ArgoNamespace string
	RepoURL       string
	Path          string
	Revision      string
}
