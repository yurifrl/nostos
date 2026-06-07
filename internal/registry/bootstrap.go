package registry

import (
	"fmt"
	"strings"
	texttemplate "text/template"

	"github.com/yurifrl/nostos/internal/config"
	"gopkg.in/yaml.v3"
)

// These mirror the constants the in-cluster controller reads
// (internal/bootstrap: ConfigMapName, SystemNamespace, ConfigMapKey). They are
// duplicated here rather than imported to keep the registry package free of the
// controller's heavy client-go dependency tree.
const (
	bootstrapConfigMapName   = "nostos-bootstrap-config"
	bootstrapSystemNamespace = "kube-system"
	bootstrapConfigMapKey    = "config.yaml"
)

// injectBootstrapManifests parses the first YAML document of the rendered
// template, sets cluster.inlineManifests to the three synthesized manifests
// (root Secret, config ConfigMap, controller bundle), and re-joins all
// documents. op:// refs inside the root Secret survive the re-marshal as plain
// scalars so the subsequent secrets.ResolveTemplate pass resolves them.
//
// It fails loud (double-emission guard) if the template still hand-writes
// cluster.inlineManifests or cluster.extraManifests while a bootstrap: block is
// configured — that signals a half-done migration.
func injectBootstrapManifests(body string, cfg *config.Config) (string, error) {
	b := cfg.Bootstrap
	docs := splitYAMLDocuments(body)
	if len(docs) == 0 {
		return "", fmt.Errorf("empty template body")
	}

	var first map[string]any
	if err := yaml.Unmarshal([]byte(docs[0]), &first); err != nil {
		return "", fmt.Errorf("parse first template document: %w", err)
	}
	cluster, ok := first["cluster"].(map[string]any)
	if !ok || cluster == nil {
		return "", fmt.Errorf("template first document has no cluster: block to attach inline manifests to")
	}
	if _, exists := cluster["inlineManifests"]; exists {
		return "", fmt.Errorf("template hand-writes cluster.inlineManifests while a bootstrap: block is configured — delete the inline manifests from the template (nostos render synthesizes them)")
	}
	if _, exists := cluster["extraManifests"]; exists {
		return "", fmt.Errorf("template hand-writes cluster.extraManifests while a bootstrap: block is configured — delete extraManifests from the template (the controller drives app-gen at runtime)")
	}

	secretM, err := rootSecretManifest(b.RootSecret)
	if err != nil {
		return "", fmt.Errorf("render root secret manifest: %w", err)
	}
	cfgM, err := bootstrapConfigManifest(b)
	if err != nil {
		return "", fmt.Errorf("render bootstrap config manifest: %w", err)
	}
	ctrlM, err := controllerBundleManifest(b.ControllerImage.Ref())
	if err != nil {
		return "", fmt.Errorf("render controller bundle manifest: %w", err)
	}
	// Deterministic order: (a) root Secret, (b) config ConfigMap, (c) controller.
	cluster["inlineManifests"] = []any{secretM, cfgM, ctrlM}

	out, err := yaml.Marshal(first)
	if err != nil {
		return "", fmt.Errorf("re-marshal first template document: %w", err)
	}

	rebuilt := append([]string{strings.TrimRight(string(out), "\n")}, docs[1:]...)
	return strings.Join(rebuilt, "\n---\n") + "\n", nil
}

// splitYAMLDocuments splits a multi-document YAML string on lines that are
// exactly "---" (after trimming). A leading separator yields an empty first
// element, which is fine: callers re-marshal only docs[0] and keep the rest
// verbatim.
func splitYAMLDocuments(body string) []string {
	lines := strings.Split(body, "\n")
	var docs []string
	var cur []string
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "---" {
			docs = append(docs, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		cur = append(cur, ln)
	}
	docs = append(docs, strings.Join(cur, "\n"))
	return docs
}

// rootSecretManifest builds the inline manifest for the irreducible
// op-credentials Secret. The op:// values go in verbatim under data: so the
// later secrets.ResolveTemplate pass replaces them with the (already
// base64-encoded) 1Password values, exactly as today's hand-written blob.
func rootSecretManifest(rs config.BootstrapRootSecret) (map[string]any, error) {
	data := make(map[string]any, len(rs.Data))
	for k, v := range rs.Data {
		data[k] = v.String() // op://... left intact for ResolveTemplate
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "Opaque",
		"metadata": map[string]any{
			"name":      rs.Name,
			"namespace": rs.Namespace,
		},
		"data": data,
	}
	contents, err := yaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"name":     "secret-op-credentials",
		"contents": string(contents),
	}, nil
}

// bootstrapPublicConfig is the non-secret subset of the bootstrap block that
// the in-cluster controller consumes. Its yaml keys MUST match
// internal/bootstrap.Config (cilium/argocd/repos/namespaces).
type bootstrapPublicConfig struct {
	Cilium     config.BootstrapCilium `yaml:"cilium"`
	Argocd     config.BootstrapArgocd `yaml:"argocd"`
	Repos      []config.BootstrapRepo `yaml:"repos,omitempty"`
	Namespaces []string               `yaml:"namespaces,omitempty"`
}

// bootstrapConfigManifest serializes the non-secret bootstrap block into a
// ConfigMap the controller reads. Secret values never appear here.
func bootstrapConfigManifest(b *config.Bootstrap) (map[string]any, error) {
	pub := bootstrapPublicConfig{
		Cilium:     b.Cilium,
		Argocd:     b.Argocd,
		Repos:      b.Repos,
		Namespaces: b.Namespaces,
	}
	cfgYAML, err := yaml.Marshal(pub)
	if err != nil {
		return nil, err
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      bootstrapConfigMapName,
			"namespace": bootstrapSystemNamespace,
		},
		"data": map[string]any{
			bootstrapConfigMapKey: string(cfgYAML),
		},
	}
	contents, err := yaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"name":     bootstrapConfigMapName,
		"contents": string(contents),
	}, nil
}

// controllerBundleTmpl is the ServiceAccount + ClusterRole + ClusterRoleBinding
// + Deployment for the nostos-bootstrap controller. It mirrors
// internal/bootstrap/manifests/controller-{rbac,deployment}.yaml. The only
// render-time substitution is the container image; everything else is static.
// Rendered in a SEPARATE text/template pass so its {{ }} never collides with
// the node-template pass (renderTemplateBody) which runs earlier.
const controllerBundleTmpl = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: nostos-bootstrap
  namespace: kube-system
  labels:
    app.kubernetes.io/name: nostos-bootstrap
    app.kubernetes.io/managed-by: nostos-bootstrap
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: nostos-bootstrap
  labels:
    app.kubernetes.io/name: nostos-bootstrap
    app.kubernetes.io/managed-by: nostos-bootstrap
rules:
  - apiGroups: ["*"]
    resources: ["*"]
    verbs: ["*"]
  - nonResourceURLs: ["*"]
    verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: nostos-bootstrap
  labels:
    app.kubernetes.io/name: nostos-bootstrap
    app.kubernetes.io/managed-by: nostos-bootstrap
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nostos-bootstrap
subjects:
  - kind: ServiceAccount
    name: nostos-bootstrap
    namespace: kube-system
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nostos-bootstrap
  namespace: kube-system
  labels:
    app.kubernetes.io/name: nostos-bootstrap
    app.kubernetes.io/managed-by: nostos-bootstrap
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: nostos-bootstrap
  strategy:
    type: Recreate
  template:
    metadata:
      labels:
        app.kubernetes.io/name: nostos-bootstrap
    spec:
      serviceAccountName: nostos-bootstrap
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      priorityClassName: system-cluster-critical
      nodeSelector:
        node-role.kubernetes.io/control-plane: ""
      tolerations:
        - key: node.kubernetes.io/not-ready
          operator: Exists
          effect: NoSchedule
        - key: node.kubernetes.io/not-ready
          operator: Exists
          effect: NoExecute
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
          effect: NoSchedule
        - key: node-role.kubernetes.io/master
          operator: Exists
          effect: NoSchedule
      containers:
        - name: controller
          image: {{ .Image }}
          imagePullPolicy: IfNotPresent
          args:
            - --interval=60s
            - --log-format=json
          env:
            - name: NOSTOS_BOOTSTRAP_NAMESPACE
              value: kube-system
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 500m
              memory: 256Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
            runAsUser: 65532
            capabilities:
              drop: ["ALL"]
`

// controllerBundleManifest renders the controller bundle with the configured
// image and wraps it as a single inline manifest.
func controllerBundleManifest(image string) (map[string]any, error) {
	tmpl, err := texttemplate.New("controller").Option("missingkey=error").Parse(controllerBundleTmpl)
	if err != nil {
		return nil, err
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, struct{ Image string }{Image: image}); err != nil {
		return nil, err
	}
	return map[string]any{
		"name":     "nostos-bootstrap-controller",
		"contents": buf.String(),
	}, nil
}
