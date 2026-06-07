package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// Options configure the controller.
type Options struct {
	// Namespace is where the bootstrap config/status/root-secret live.
	Namespace string
	// Interval is the periodic self-heal reconcile cadence (design §5/§4.1).
	Interval time.Duration
	// Logger is the structured logger; one line per reconcile decision.
	Logger *slog.Logger
}

func (o *Options) withDefaults() {
	if o.Namespace == "" {
		o.Namespace = SystemNamespace
	}
	if o.Interval <= 0 {
		o.Interval = 60 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Controller is the self-healing bootstrap reconciler (design §4.1). It owns
// tiers 1-4 (Cilium, ESO, ArgoCD, generated root apps) and hands off to ArgoCD.
type Controller struct {
	kube    kubernetes.Interface
	dyn     dynamic.Interface
	disco   discovery.DiscoveryInterface
	applier *applier
	log     *slog.Logger

	namespace string
	interval  time.Duration
}

// New builds a Controller from a rest.Config (typically rest.InClusterConfig).
func New(cfg *rest.Config, opts Options) (*Controller, error) {
	opts.withDefaults()

	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	disco, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return &Controller{
		kube:      kube,
		dyn:       dyn,
		disco:     disco,
		applier:   newApplier(dyn, disco, opts.Logger),
		log:       opts.Logger,
		namespace: opts.Namespace,
		interval:  opts.Interval,
	}, nil
}

// Run drives the reconcile loop until ctx is cancelled. It reconciles
// immediately, then on a ticker (self-heal) and on informer-driven changes to
// nodes (CNI readiness) — design §5 "interval + watch".
func (c *Controller) Run(ctx context.Context) error {
	c.log.Info("nostos-bootstrap controller starting",
		"namespace", c.namespace,
		"interval", c.interval.String(),
		"fieldManager", FieldManager,
	)

	// Coalesced trigger channel: a buffered size-1 channel collapses bursts of
	// informer events into at most one pending reconcile.
	trigger := make(chan struct{}, 1)
	notify := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}

	stopInformers := c.startInformers(ctx, notify)
	defer stopInformers()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Initial reconcile.
	c.reconcileOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			c.log.Info("controller shutting down", "reason", ctx.Err())
			return ctx.Err()
		case <-ticker.C:
			c.reconcileOnce(ctx)
		case <-trigger:
			c.log.Debug("informer-triggered reconcile")
			c.reconcileOnce(ctx)
		}
	}
}

// startInformers wires a node informer that fires notify on any change, for
// self-heal responsiveness (design §5 watch). Returns a stop func.
func (c *Controller) startInformers(ctx context.Context, notify func()) func() {
	factory := informers.NewSharedInformerFactory(c.kube, 10*time.Minute)
	nodeInformer := factory.Core().V1().Nodes().Informer()
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { notify() },
		UpdateFunc: func(_, _ any) { notify() },
		DeleteFunc: func(any) { notify() },
	}
	if _, err := nodeInformer.AddEventHandler(handler); err != nil {
		c.log.Warn("failed to register node informer handler", "err", err)
	}
	stopCh := make(chan struct{})
	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)
	return func() { close(stopCh) }
}

// reconcileOnce runs the ordered state machine once and persists status. It
// never panics the loop: every tier failure is logged + recorded and the next
// interval retries (self-heal, design §5/§6).
func (c *Controller) reconcileOnce(ctx context.Context) {
	status := newStatus()
	status.LastReconcile = time.Now().UTC()

	cfg, err := LoadConfig(ctx, c.kube, c.namespace)
	if err != nil {
		status.LastError = err.Error()
		c.log.Error("reconcile aborted: cannot load config", "err", err)
		c.persist(ctx, status)
		return
	}

	c.reconcile(ctx, cfg, status)
	c.persist(ctx, status)

	if status.allReady() {
		c.log.Info("reconcile complete: all tiers Ready")
	} else {
		c.log.Info("reconcile complete: tiers pending",
			"cilium", status.Cilium.State,
			"eso", status.ESO.State,
			"argocd", status.ArgoCD.State,
			"apps", status.Apps.State,
		)
	}
}

// reconcile is the ordered tier state machine (design §5). Each tier gates the
// next: a failed wait stops the sequence and the next reconcile retries from
// the top (idempotent).
func (c *Controller) reconcile(ctx context.Context, cfg *Config, status *Status) {
	if err := c.ensureNamespaces(ctx, cfg); err != nil {
		c.fail(status, &status.Cilium, "namespaces", err)
		return
	}

	// Tier 1: Cilium -> nodes Ready.
	if !c.runTier(ctx, "cilium", &status.Cilium, status,
		func(ctx context.Context) error { return c.applyCilium(ctx, cfg) },
		func(ctx context.Context) error { return c.waitNodesReady(ctx) },
	) {
		return
	}

	// Tier 2: ESO + ClusterSecretStore -> Valid.
	if !c.runTier(ctx, "eso", &status.ESO, status,
		func(ctx context.Context) error { return c.applyESO(ctx, cfg) },
		func(ctx context.Context) error { return c.waitStoreValid(ctx, cfg) },
	) {
		return
	}

	// Tier 3: ArgoCD -> healthy.
	if !c.runTier(ctx, "argocd", &status.ArgoCD, status,
		func(ctx context.Context) error { return c.applyArgoCD(ctx, cfg) },
		func(ctx context.Context) error { return c.waitArgoCDHealthy(ctx, cfg) },
	) {
		return
	}

	// Tier 4: generate root app(s). No wait — ArgoCD owns sync from here.
	if !c.runTier(ctx, "apps", &status.Apps, status,
		func(ctx context.Context) error { return c.generateApps(ctx, cfg) },
		nil,
	) {
		return
	}
}

// runTier applies a tier, optionally waits for it, and records status. Returns
// true on success so the caller can gate the next tier.
func (c *Controller) runTier(
	ctx context.Context,
	name string,
	tier *TierStatus,
	status *Status,
	apply func(context.Context) error,
	wait func(context.Context) error,
) bool {
	tier.set(TierApplying, "applying")
	c.log.Info("tier apply", "tier", name)
	if err := apply(ctx); err != nil {
		c.fail(status, tier, name, err)
		return false
	}
	if wait != nil {
		c.log.Info("tier wait", "tier", name)
		if err := wait(ctx); err != nil {
			c.fail(status, tier, name, err)
			return false
		}
	}
	tier.set(TierReady, "ready")
	c.log.Info("tier ready", "tier", name)
	return true
}

// fail records a tier failure and rolls it up into the status LastError.
func (c *Controller) fail(status *Status, tier *TierStatus, name string, err error) {
	tier.set(TierFailed, err.Error())
	status.LastError = name + ": " + err.Error()
	c.log.Error("tier failed", "tier", name, "err", err)
}

// persist writes the status ConfigMap, logging (not failing) on error.
func (c *Controller) persist(ctx context.Context, status *Status) {
	if err := writeStatus(ctx, c.kube, c.namespace, status); err != nil {
		c.log.Error("failed to write status configmap", "err", err)
	}
}
