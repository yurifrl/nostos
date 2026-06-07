package bootstrap

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Per-tier wait timeouts (design §5: each step gates the next with a bound).
const (
	waitNodesReadyTimeout = 5 * time.Minute
	waitStoreValidTimeout = 3 * time.Minute
	waitArgoCDTimeout     = 5 * time.Minute
	waitPollInterval      = 5 * time.Second
)

// gvrClusterSecretStore / gvrApplication are the dynamic GVRs the controller
// reads while waiting on tiers it just applied.
var (
	gvrClusterSecretStore = schema.GroupVersionResource{
		Group: "external-secrets.io", Version: "v1", Resource: "clustersecretstores",
	}
	gvrArgoApplication = schema.GroupVersionResource{
		Group: "argoproj.io", Version: "v1alpha1", Resource: "applications",
	}
)

// ensureNamespaces SSA-applies the config-declared namespaces (design §3.1
// "root namespaces"). kube-system/argocd are always ensured too.
func (c *Controller) ensureNamespaces(ctx context.Context, cfg *Config) error {
	want := map[string]struct{}{SystemNamespace: {}, cfg.argoNamespace(): {}}
	for _, ns := range cfg.Namespaces {
		if ns != "" {
			want[ns] = struct{}{}
		}
	}
	for ns := range want {
		obj := &corev1.Namespace{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}
		if _, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err == nil {
			continue
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get namespace %s: %w", ns, err)
		}
		if _, err := c.kube.CoreV1().Namespaces().Create(ctx, obj, metav1.CreateOptions{FieldManager: FieldManager}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create namespace %s: %w", ns, err)
		}
	}
	return nil
}

// --- Tier 1: Cilium (CNI) ---

func (c *Controller) applyCilium(ctx context.Context, cfg *Config) error {
	data, err := readManifest(manifestCilium)
	if err != nil {
		return err
	}
	if isPlaceholder(data) {
		c.log.Warn("cilium manifest is a TODO placeholder; applying nothing", "tier", "cilium")
	}
	if _, err := c.applier.applyYAML(ctx, data); err != nil {
		return err
	}
	return nil
}

// waitNodesReady blocks until every node reports Ready (CNI up). design §5.1.
func (c *Controller) waitNodesReady(ctx context.Context) error {
	return poll(ctx, waitNodesReadyTimeout, func(ctx context.Context) (bool, error) {
		nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		if len(nodes.Items) == 0 {
			return false, nil
		}
		for _, n := range nodes.Items {
			if !nodeReady(&n) {
				return false, nil
			}
		}
		return true, nil
	})
}

func nodeReady(n *corev1.Node) bool {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// --- Tier 2: ESO + ClusterSecretStore ---

func (c *Controller) applyESO(ctx context.Context, cfg *Config) error {
	// Operator + CRDs (TODO placeholder until rendered).
	data, err := readManifest(manifestESO)
	if err != nil {
		return err
	}
	if isPlaceholder(data) {
		c.log.Warn("eso manifest is a TODO placeholder; applying nothing", "tier", "eso")
	}
	if _, err := c.applier.applyYAML(ctx, data); err != nil {
		return err
	}

	// The root Secret must exist before the store references it (design risk #3).
	if _, err := LoadRootSecret(ctx, c.kube, SystemNamespace, cfg.rootSecretName()); err != nil {
		return fmt.Errorf("root secret precondition: %w", err)
	}

	// ClusterSecretStore (rendered from the root Secret).
	store, err := renderTemplate(templateStore, storeTemplateData{
		StoreName:       cfg.clusterSecretStoreName(),
		SecretName:      cfg.rootSecretName(),
		SecretNamespace: SystemNamespace,
	})
	if err != nil {
		return err
	}
	// The store maps only once the ESO CRDs are installed; skip-on-no-match so a
	// placeholder ESO tier doesn't hard-fail the reconcile.
	if _, err := c.applier.applyYAML(ctx, store); err != nil {
		c.log.Warn("clustersecretstore apply skipped (ESO CRDs not present yet?)", "err", err)
	}
	return nil
}

// waitStoreValid blocks until the ClusterSecretStore reports Ready=True.
// design §5.2. A missing CRD (placeholder ESO tier) is treated as not-ready,
// not as a hard error, so the scaffold reconciles cleanly.
func (c *Controller) waitStoreValid(ctx context.Context, cfg *Config) error {
	name := cfg.clusterSecretStoreName()
	return poll(ctx, waitStoreValidTimeout, func(ctx context.Context) (bool, error) {
		obj, err := c.dyn.Resource(gvrClusterSecretStore).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			// CRD not registered yet — treat as not-ready (placeholder tier).
			return false, nil
		}
		return conditionTrue(obj.Object, "Ready"), nil
	})
}

// --- Tier 3: ArgoCD ---

func (c *Controller) applyArgoCD(ctx context.Context, cfg *Config) error {
	data, err := readManifest(manifestArgoCD)
	if err != nil {
		return err
	}
	if isPlaceholder(data) {
		c.log.Warn("argocd manifest is a TODO placeholder; applying nothing", "tier", "argocd")
	}
	if _, err := c.applier.applyYAML(ctx, data); err != nil {
		return err
	}
	return nil
}

// waitArgoCDHealthy blocks until the argocd-server Deployment is available.
// design §5.3.
func (c *Controller) waitArgoCDHealthy(ctx context.Context, cfg *Config) error {
	ns := cfg.argoNamespace()
	return poll(ctx, waitArgoCDTimeout, func(ctx context.Context) (bool, error) {
		dep, err := c.kube.AppsV1().Deployments(ns).Get(ctx, "argocd-server", metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, nil
		}
		return dep.Status.AvailableReplicas >= 1, nil
	})
}

// --- Tier 4: generated root Application(s) ---

// generateApps renders and applies ONE root app-of-apps ArgoCD Application per
// config.repos entry (design §4.4). Idempotent via SSA.
func (c *Controller) generateApps(ctx context.Context, cfg *Config) error {
	if len(cfg.Repos) == 0 {
		c.log.Info("no repos configured; no root apps to generate", "tier", "apps")
		return nil
	}
	for i, repo := range cfg.Repos {
		if repo.URL == "" {
			return fmt.Errorf("repos[%d]: url is required", i)
		}
		rev := repo.Revision
		if rev == "" {
			rev = "HEAD"
		}
		app, err := renderTemplate(templateRootApp, appTemplateData{
			Name:          appName(i, repo),
			ArgoNamespace: cfg.argoNamespace(),
			RepoURL:       repo.URL,
			Path:          repo.Path,
			Revision:      rev,
		})
		if err != nil {
			return err
		}
		if _, err := c.applier.applyYAML(ctx, app); err != nil {
			return fmt.Errorf("apply root app for %s: %w", repo.URL, err)
		}
	}
	return nil
}

// appName derives a stable root-app name for a repo entry.
func appName(i int, repo RepoEntry) string {
	return fmt.Sprintf("nostos-root-%d", i)
}

// --- helpers ---

// conditionTrue reports whether an unstructured object has a status condition
// of the given type with status "True".
func conditionTrue(obj map[string]any, condType string) bool {
	status, ok := obj["status"].(map[string]any)
	if !ok {
		return false
	}
	conds, ok := status["conditions"].([]any)
	if !ok {
		return false
	}
	for _, raw := range conds {
		cond, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == condType && cond["status"] == "True" {
			return true
		}
	}
	return false
}

// poll invokes fn every waitPollInterval until it returns true, errors, the
// timeout elapses, or ctx is cancelled.
func poll(ctx context.Context, timeout time.Duration, fn func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()
	for {
		done, err := fn(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait timed out after %s: %w", timeout, ctx.Err())
		case <-ticker.C:
		}
	}
}
