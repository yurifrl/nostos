// Package bootstrap implements the in-cluster nostos-bootstrap controller: a
// self-healing reconcile loop that drives a fresh Talos cluster from bare
// apiserver to "ArgoCD reconciling the user's repo".
//
// Ordering (design §5): Cilium (CNI) -> ESO + ClusterSecretStore -> ArgoCD ->
// generated root Application(s). Each tier gates the next with wait + timeout;
// the loop is idempotent and re-applies anything that drifts (self-heal).
//
// The controller applies embedded, parameterized manifests via client-go
// Server-Side Apply (FieldManager "nostos-bootstrap"). No Helm at runtime.
package bootstrap

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

const (
	// FieldManager is the Server-Side Apply field manager for every object the
	// controller owns (design §4.1).
	FieldManager = "nostos-bootstrap"

	// SystemNamespace is where the bootstrap config/status/root-secret live.
	SystemNamespace = "kube-system"

	// ConfigMapName is the desired-state ConfigMap (rendered from config.yaml).
	ConfigMapName = "nostos-bootstrap-config"

	// StatusConfigMapName is the rolled-up per-tier status surface (design §7).
	StatusConfigMapName = "nostos-bootstrap-status"

	// ConfigMapKey is the data key holding the serialized Config YAML.
	ConfigMapKey = "config.yaml"

	// DefaultRootSecretName is the name of the irreducible root Secret
	// (design risk #3) consumed by the ClusterSecretStore.
	DefaultRootSecretName = "nostos-bootstrap-root"

	// DefaultArgoNamespace is where ArgoCD and the generated root apps live.
	DefaultArgoNamespace = "argocd"

	// DefaultClusterSecretStore is the generated store's name.
	DefaultClusterSecretStore = "nostos-bootstrap"
)

// Config is the controller's desired state, deserialized from the
// nostos-bootstrap-config ConfigMap (which nostos renders from the
// config.yaml `bootstrap:` block). See design §3.1.
type Config struct {
	Cilium     CiliumConfig `yaml:"cilium" json:"cilium"`
	ArgoCD     ArgoCDConfig `yaml:"argocd" json:"argocd"`
	Repos      []RepoEntry  `yaml:"repos,omitempty" json:"repos,omitempty"`
	Namespaces []string     `yaml:"namespaces,omitempty" json:"namespaces,omitempty"`

	// RootSecretName overrides DefaultRootSecretName when set.
	RootSecretName string `yaml:"rootSecretName,omitempty" json:"rootSecretName,omitempty"`
	// ClusterSecretStoreName overrides DefaultClusterSecretStore when set.
	ClusterSecretStoreName string `yaml:"clusterSecretStoreName,omitempty" json:"clusterSecretStoreName,omitempty"`
}

// CiliumConfig parameterizes the Cilium (CNI) tier.
type CiliumConfig struct {
	Version string         `yaml:"version,omitempty" json:"version,omitempty"`
	Values  map[string]any `yaml:"values,omitempty" json:"values,omitempty"`
}

// ArgoCDConfig parameterizes the ArgoCD tier.
type ArgoCDConfig struct {
	Version   string `yaml:"version,omitempty" json:"version,omitempty"`
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
}

// RepoEntry is one user GitOps repo. The controller generates exactly one root
// app-of-apps ArgoCD Application per entry (design §4.4).
type RepoEntry struct {
	URL      string `yaml:"url" json:"url"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Revision string `yaml:"revision,omitempty" json:"revision,omitempty"`
}

// argoNamespace returns the effective ArgoCD namespace.
func (c *Config) argoNamespace() string {
	if c.ArgoCD.Namespace != "" {
		return c.ArgoCD.Namespace
	}
	return DefaultArgoNamespace
}

// rootSecretName returns the effective root Secret name.
func (c *Config) rootSecretName() string {
	if c.RootSecretName != "" {
		return c.RootSecretName
	}
	return DefaultRootSecretName
}

// clusterSecretStoreName returns the effective ClusterSecretStore name.
func (c *Config) clusterSecretStoreName() string {
	if c.ClusterSecretStoreName != "" {
		return c.ClusterSecretStoreName
	}
	return DefaultClusterSecretStore
}

// LoadConfig reads and parses the bootstrap-config ConfigMap from the cluster.
func LoadConfig(ctx context.Context, kube kubernetes.Interface, namespace string) (*Config, error) {
	cm, err := kube.CoreV1().ConfigMaps(namespace).Get(ctx, ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get configmap %s/%s: %w", namespace, ConfigMapName, err)
	}
	raw, ok := cm.Data[ConfigMapKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s/%s missing key %q", namespace, ConfigMapName, ConfigMapKey)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("parse %s key %q: %w", ConfigMapName, ConfigMapKey, err)
	}
	return &cfg, nil
}

// LoadRootSecret fetches the irreducible root Secret (design risk #3). It is
// consumed by the ClusterSecretStore tier; the controller only reads it.
func LoadRootSecret(ctx context.Context, kube kubernetes.Interface, namespace, name string) (*corev1.Secret, error) {
	sec, err := kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get root secret %s/%s: %w", namespace, name, err)
	}
	return sec, nil
}
