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

// Controller is the bootstrap-to-handoff reconciler (design §4.1/§6). It drives
// tiers 1-4 (Cilium, ESO, ArgoCD, generated root apps) up *just enough to stop
// blocking anyone*, hands ownership to ArgoCD, and then shrinks to a watchdog
// on ArgoCD only. It is NOT a perpetual reconciler: a handed-off tier is never
// re-applied (ArgoCD owns it).
type Controller struct {
	kube    kubernetes.Interface
	dyn     dynamic.Interface
	disco   discovery.DiscoveryInterface
	applier *applier
	log     *slog.Logger

	namespace string
	interval  time.Duration

	// phases is the durable bootstrap-to-handoff state per tier (design §6),
	// restored from the status ConfigMap on startup so handoff survives restarts.
	// The reconcile loop is single-goroutine (ticker + coalesced trigger in one
	// select), so this needs no locking.
	phases map[string]TierPhase
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
		phases: map[string]TierPhase{
			tierCilium: PhasePending,
			tierESO:    PhasePending,
			tierArgoCD: PhasePending,
			tierApps:   PhasePending,
		},
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

	// Restore durable handoff phases from the status ConfigMap so a controller
	// restart does not re-apply tiers ArgoCD already owns (design §6).
	c.restorePhases(ctx)

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

// reconcileOnce runs the bootstrap-to-handoff state machine once and persists
// status. It never panics the loop: every tier failure is logged + recorded
// and the next interval retries (self-heal, design §6).
func (c *Controller) reconcileOnce(ctx context.Context) {
	status := newStatus()
	status.LastReconcile = time.Now().UTC()
	c.seedPhases(status) // reflect durable handoff phases into this snapshot

	cfg, err := LoadConfig(ctx, c.kube, c.namespace)
	if err != nil {
		status.LastError = err.Error()
		c.log.Error("reconcile aborted: cannot load config", "err", err)
		c.persist(ctx, status)
		return
	}

	c.reconcile(ctx, cfg, status)
	c.persist(ctx, status)

	switch {
	case c.allHandedOff():
		c.log.Info("reconcile complete: all tiers handed off to ArgoCD")
	case status.allReady():
		c.log.Info("reconcile complete: all tiers Ready")
	default:
		c.log.Info("reconcile complete: tiers pending",
			"cilium", c.phases[tierCilium],
			"eso", c.phases[tierESO],
			"argocd", c.phases[tierArgoCD],
			"apps", c.phases[tierApps],
		)
	}
}

// reconcile is the bootstrap-to-handoff state machine (design §6).
//
//   - Steady state (all tiers handed off): a single cheap ArgoCD health check.
//     Healthy -> idle, touch nothing. Unhealthy -> reset handoff and re-engage
//     the ordered bootstrap to get ArgoCD back.
//   - Otherwise: the ordered sequence (Cilium -> ESO -> ArgoCD -> apps), but
//     any tier already handed off is skipped entirely (ArgoCD owns it). Each
//     tier gates the next.
func (c *Controller) reconcile(ctx context.Context, cfg *Config, status *Status) {
	// Steady state = watchdog on ArgoCD only (design §6). Returns true when the
	// tick is idle (nothing to do); false means re-engage the ordered bootstrap.
	if c.handleSteadyState(ctx, cfg, status) {
		return
	}

	if err := c.ensureNamespaces(ctx, cfg); err != nil {
		c.fail(status, &status.Cilium, "namespaces", err)
		return
	}

	// Tier 1: Cilium -> nodes Ready.
	if !c.runTier(ctx, cfg, tierCilium, &status.Cilium, status,
		func(ctx context.Context) error { return c.applyCilium(ctx, cfg) },
		c.nodesReady, waitNodesReadyTimeout,
	) {
		return
	}

	// Tier 2: ESO + ClusterSecretStore -> Valid.
	if !c.runTier(ctx, cfg, tierESO, &status.ESO, status,
		func(ctx context.Context) error { return c.applyESO(ctx, cfg) },
		func(ctx context.Context) (bool, error) { return c.storeValid(ctx, cfg) }, waitStoreValidTimeout,
	) {
		return
	}

	// Tier 3: ArgoCD -> healthy (this IS the handoff target).
	if !c.runTier(ctx, cfg, tierArgoCD, &status.ArgoCD, status,
		func(ctx context.Context) error { return c.applyArgoCD(ctx, cfg) },
		func(ctx context.Context) (bool, error) { return c.argoCDHealthy(ctx, cfg) }, waitArgoCDTimeout,
	) {
		return
	}

	// Tier 4: generate root app(s) -> present. ArgoCD owns sync from here.
	if !c.runTier(ctx, cfg, tierApps, &status.Apps, status,
		func(ctx context.Context) error { return c.generateApps(ctx, cfg) },
		func(ctx context.Context) (bool, error) { return c.rootAppsPresent(ctx, cfg) }, waitAppsTimeout,
	) {
		return
	}
}

// handleSteadyState implements the watchdog (design §6). When every tier is
// handed off it does ONE cheap check — is ArgoCD healthy?
//
//   - not all handed off -> returns false (bootstrap still in progress).
//   - all handed off + ArgoCD healthy -> idle: record the idle snapshot and
//     return true (touch nothing in-cluster).
//   - all handed off + ArgoCD unhealthy -> reset handoff (re-engage) and return
//     false so the caller runs the ordered bootstrap to restore ArgoCD.
func (c *Controller) handleSteadyState(ctx context.Context, cfg *Config, status *Status) (idle bool) {
	if !c.allHandedOff() {
		return false
	}
	if healthy, _ := c.argoCDHealthy(ctx, cfg); healthy {
		c.log.Debug("steady state: all tiers handed off, argocd healthy; idle")
		c.markHandedOffIdle(status)
		return true
	}
	c.log.Warn("watchdog: argocd unhealthy in steady state; re-engaging bootstrap")
	c.resetHandoff(status)
	return false
}

// runTier drives one tier through the handoff state machine and records status.
// Returns true so the caller can gate the next tier.
//
//   - handed-off  -> skip entirely (ArgoCD owns it; never re-apply, design §6).
//   - already up  -> advance phases without re-applying (avoids competing with
//     ArgoCD during the unblocked->handed-off window).
//   - not up yet  -> apply, then block until the unblock predicate is met.
//
// predicate is the non-blocking unblock condition (design §6); poll turns it
// into the blocking wait used during active bootstrap.
func (c *Controller) runTier(
	ctx context.Context,
	cfg *Config,
	name string,
	tier *TierStatus,
	status *Status,
	apply func(context.Context) error,
	predicate func(context.Context) (bool, error),
	timeout time.Duration,
) bool {
	if c.phases[name] == PhaseHandedOff {
		tier.set(TierReady, "handed off to ArgoCD")
		tier.setPhase(PhaseHandedOff)
		c.log.Debug("tier skipped: handed off to ArgoCD", "tier", name)
		return true
	}

	// Cheap pre-check: already up? Then don't re-apply, just advance phases.
	if up, err := predicate(ctx); err != nil {
		c.fail(status, tier, name, err)
		return false
	} else if up {
		c.advanceUnblocked(name, tier)
		c.maybeHandoff(ctx, cfg, name, tier)
		tier.set(TierReady, "ready")
		return true
	}

	// Not up yet: apply, then block until the unblock condition is satisfied.
	tier.set(TierApplying, "applying")
	c.log.Info("tier apply", "tier", name)
	if err := apply(ctx); err != nil {
		c.fail(status, tier, name, err)
		return false
	}
	c.log.Info("tier wait", "tier", name)
	if err := poll(ctx, timeout, predicate); err != nil {
		c.fail(status, tier, name, err)
		return false
	}
	c.advanceUnblocked(name, tier)
	c.maybeHandoff(ctx, cfg, name, tier)
	tier.set(TierReady, "ready")
	return true
}

// advanceUnblocked moves a pending tier to unblocked, logging the transition
// exactly once (design §6: one slog line per phase transition).
func (c *Controller) advanceUnblocked(name string, tier *TierStatus) {
	if c.phases[name] == PhasePending {
		c.phases[name] = PhaseUnblocked
		c.log.Info("tier phase transition", "tier", name, "from", PhasePending, "to", PhaseUnblocked)
	}
	tier.setPhase(c.phases[name])
}

// maybeHandoff transitions an unblocked tier to handed-off once ArgoCD (the
// day-2 owner) is healthy (design §6 handoff rule). One slog line on
// transition. ArgoCD must exist before any tier (including ArgoCD itself) hands
// off, so this re-checks ArgoCD health freshly.
func (c *Controller) maybeHandoff(ctx context.Context, cfg *Config, name string, tier *TierStatus) {
	if c.phases[name] == PhaseHandedOff {
		return
	}
	if healthy, _ := c.argoCDHealthy(ctx, cfg); !healthy {
		return
	}
	c.phases[name] = PhaseHandedOff
	tier.setPhase(PhaseHandedOff)
	c.log.Info("tier phase transition", "tier", name, "from", PhaseUnblocked, "to", PhaseHandedOff)
}

// allHandedOff reports whether every tier is handed off (steady state).
func (c *Controller) allHandedOff() bool {
	for _, p := range c.phases {
		if p != PhaseHandedOff {
			return false
		}
	}
	return true
}

// resetHandoff re-engages bootstrap: every tier returns to pending so the
// ordered sequence runs again to restore ArgoCD (design §6 watchdog
// re-engage). Emits one slog line for the event.
func (c *Controller) resetHandoff(status *Status) {
	for name := range c.phases {
		c.phases[name] = PhasePending
	}
	c.seedPhases(status)
	c.log.Warn("handoff reset: re-engaging ordered bootstrap", "reason", "argocd-unhealthy")
}

// seedPhases mirrors the durable handoff phases into a status snapshot so the
// persisted ConfigMap always reflects the current phase, even for tiers not
// processed this tick.
func (c *Controller) seedPhases(status *Status) {
	status.Cilium.setPhase(c.phases[tierCilium])
	status.ESO.setPhase(c.phases[tierESO])
	status.ArgoCD.setPhase(c.phases[tierArgoCD])
	status.Apps.setPhase(c.phases[tierApps])
}

// markHandedOffIdle records the steady-state idle snapshot: all tiers Ready and
// handed off. The controller touches nothing in-cluster on an idle tick.
func (c *Controller) markHandedOffIdle(status *Status) {
	for _, t := range []*TierStatus{&status.Cilium, &status.ESO, &status.ArgoCD, &status.Apps} {
		t.set(TierReady, "handed off to ArgoCD (idle)")
		t.setPhase(PhaseHandedOff)
	}
}

// restorePhases loads durable handoff phases from the status ConfigMap on
// startup so a controller restart does not re-apply tiers ArgoCD already owns
// (design §6). A missing or unparseable status is not an error: every tier
// defaults to pending.
func (c *Controller) restorePhases(ctx context.Context) {
	prev, err := readStatus(ctx, c.kube, c.namespace)
	if err != nil {
		c.log.Info("no prior bootstrap status to restore; starting all tiers pending", "err", err)
		return
	}
	c.phases[tierCilium] = normalizePhase(prev.Cilium.Phase)
	c.phases[tierESO] = normalizePhase(prev.ESO.Phase)
	c.phases[tierArgoCD] = normalizePhase(prev.ArgoCD.Phase)
	c.phases[tierApps] = normalizePhase(prev.Apps.Phase)
	c.log.Info("restored bootstrap handoff phases",
		"cilium", c.phases[tierCilium],
		"eso", c.phases[tierESO],
		"argocd", c.phases[tierArgoCD],
		"apps", c.phases[tierApps],
	)
}

// normalizePhase coerces an unknown/empty persisted phase to PhasePending.
func normalizePhase(p TierPhase) TierPhase {
	switch p {
	case PhaseUnblocked, PhaseHandedOff:
		return p
	default:
		return PhasePending
	}
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
