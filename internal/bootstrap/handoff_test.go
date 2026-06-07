package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
)

// newTestController builds a Controller wired to a fake kube client with all
// tiers pending. dyn/disco/applier are nil: the handoff state-machine tests
// drive runTier with stub apply/predicate funcs and only exercise the kube
// client via argoCDHealthy.
func newTestController(t *testing.T, objs ...runtime.Object) *Controller {
	t.Helper()
	kube := kfake.NewSimpleClientset(objs...)
	return &Controller{
		kube:      kube,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		namespace: SystemNamespace,
		phases: map[string]TierPhase{
			tierCilium: PhasePending,
			tierESO:    PhasePending,
			tierArgoCD: PhasePending,
			tierApps:   PhasePending,
		},
	}
}

// argoServer returns an available argocd-server Deployment in the given ns.
func argoServer(ns string, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: ns},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: available},
	}
}

func defaultCfg() *Config { return &Config{} }

// --- pure helpers ---

func TestNormalizePhase(t *testing.T) {
	cases := map[TierPhase]TierPhase{
		PhasePending:         PhasePending,
		PhaseUnblocked:       PhaseUnblocked,
		PhaseHandedOff:       PhaseHandedOff,
		TierPhase(""):        PhasePending,
		TierPhase("garbage"): PhasePending,
	}
	for in, want := range cases {
		if got := normalizePhase(in); got != want {
			t.Errorf("normalizePhase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAllHandedOff(t *testing.T) {
	c := newTestController(t)
	if c.allHandedOff() {
		t.Fatal("fresh controller (all pending) must not be all-handed-off")
	}
	for _, k := range []string{tierCilium, tierESO, tierArgoCD, tierApps} {
		c.phases[k] = PhaseHandedOff
	}
	if !c.allHandedOff() {
		t.Fatal("all phases handed-off must report allHandedOff")
	}
	c.phases[tierESO] = PhaseUnblocked
	if c.allHandedOff() {
		t.Fatal("one unblocked tier must break allHandedOff")
	}
}

func TestResetHandoff(t *testing.T) {
	c := newTestController(t)
	for _, k := range []string{tierCilium, tierESO, tierArgoCD, tierApps} {
		c.phases[k] = PhaseHandedOff
	}
	status := newStatus()
	c.resetHandoff(status)
	for k, p := range c.phases {
		if p != PhasePending {
			t.Errorf("phase %q = %q after reset, want pending", k, p)
		}
	}
	if status.ArgoCD.Phase != PhasePending {
		t.Errorf("status not re-seeded after reset: argocd phase = %q", status.ArgoCD.Phase)
	}
}

// --- runTier handoff state machine ---

func TestRunTier_SkipsHandedOff(t *testing.T) {
	c := newTestController(t, argoServer(DefaultArgoNamespace, 1))
	c.phases[tierCilium] = PhaseHandedOff
	status := newStatus()
	applied := false
	ok := c.runTier(context.Background(), defaultCfg(), tierCilium, &status.Cilium, status,
		func(context.Context) error { applied = true; return nil },
		func(context.Context) (bool, error) { return true, nil },
		waitNodesReadyTimeout,
	)
	if !ok {
		t.Fatal("handed-off tier should return ok=true")
	}
	if applied {
		t.Fatal("handed-off tier must NOT be applied (ArgoCD owns it)")
	}
	if c.phases[tierCilium] != PhaseHandedOff {
		t.Fatalf("phase = %q, want handed-off", c.phases[tierCilium])
	}
}

// Already up + ArgoCD healthy -> hand off without applying.
func TestRunTier_AlreadyUp_HandsOff(t *testing.T) {
	c := newTestController(t, argoServer(DefaultArgoNamespace, 1))
	status := newStatus()
	applied := false
	ok := c.runTier(context.Background(), defaultCfg(), tierCilium, &status.Cilium, status,
		func(context.Context) error { applied = true; return nil },
		func(context.Context) (bool, error) { return true, nil },
		waitNodesReadyTimeout,
	)
	if !ok || applied {
		t.Fatalf("ok=%v applied=%v; want ok=true, applied=false (already up)", ok, applied)
	}
	if c.phases[tierCilium] != PhaseHandedOff {
		t.Fatalf("phase = %q, want handed-off (up + argocd healthy)", c.phases[tierCilium])
	}
}

// Already up but ArgoCD NOT healthy -> unblocked, not handed off.
func TestRunTier_AlreadyUp_ArgoUnhealthy_StaysUnblocked(t *testing.T) {
	c := newTestController(t) // no argocd-server deployment
	status := newStatus()
	ok := c.runTier(context.Background(), defaultCfg(), tierCilium, &status.Cilium, status,
		func(context.Context) error { return nil },
		func(context.Context) (bool, error) { return true, nil },
		waitNodesReadyTimeout,
	)
	if !ok {
		t.Fatal("want ok=true")
	}
	if c.phases[tierCilium] != PhaseUnblocked {
		t.Fatalf("phase = %q, want unblocked (up but argocd unhealthy)", c.phases[tierCilium])
	}
}

// Not up initially -> apply, then poll sees it come up; ArgoCD healthy -> hand off.
func TestRunTier_NotUp_AppliesThenHandsOff(t *testing.T) {
	c := newTestController(t, argoServer(DefaultArgoNamespace, 1))
	status := newStatus()
	applied := false
	calls := 0
	ok := c.runTier(context.Background(), defaultCfg(), tierCilium, &status.Cilium, status,
		func(context.Context) error { applied = true; return nil },
		func(context.Context) (bool, error) {
			// pre-check (call 0) reports down; after apply, poll's first call reports up.
			calls++
			return calls > 1, nil
		},
		waitNodesReadyTimeout,
	)
	if !ok || !applied {
		t.Fatalf("ok=%v applied=%v; want both true (apply path)", ok, applied)
	}
	if c.phases[tierCilium] != PhaseHandedOff {
		t.Fatalf("phase = %q, want handed-off", c.phases[tierCilium])
	}
}

// --- reconcile steady-state watchdog ---

// All handed off + ArgoCD healthy -> idle: nothing applied, phases preserved.
func TestHandleSteadyState_Idle(t *testing.T) {
	c := newTestController(t, argoServer(DefaultArgoNamespace, 1))
	for _, k := range []string{tierCilium, tierESO, tierArgoCD, tierApps} {
		c.phases[k] = PhaseHandedOff
	}
	status := newStatus()
	c.seedPhases(status)
	if idle := c.handleSteadyState(context.Background(), defaultCfg(), status); !idle {
		t.Fatal("argocd healthy in steady state must be idle")
	}
	if !c.allHandedOff() {
		t.Fatal("idle steady state must preserve all-handed-off phases")
	}
	if status.Cilium.State != TierReady || status.ArgoCD.State != TierReady {
		t.Fatalf("idle status not Ready: cilium=%q argocd=%q", status.Cilium.State, status.ArgoCD.State)
	}
	if status.LastError != "" {
		t.Fatalf("idle reconcile recorded error: %q", status.LastError)
	}
}

// All handed off + ArgoCD unhealthy -> re-engage: not idle, phases reset.
func TestHandleSteadyState_ReengageOnArgoLoss(t *testing.T) {
	c := newTestController(t) // no argocd-server -> unhealthy
	for _, k := range []string{tierCilium, tierESO, tierArgoCD, tierApps} {
		c.phases[k] = PhaseHandedOff
	}
	status := newStatus()
	c.seedPhases(status)
	if idle := c.handleSteadyState(context.Background(), defaultCfg(), status); idle {
		t.Fatal("argocd loss must NOT be idle (must re-engage)")
	}
	if c.allHandedOff() {
		t.Fatal("argocd loss in steady state must reset handoff (re-engage)")
	}
}

// Not all handed off -> watchdog is a no-op (bootstrap still in progress).
func TestHandleSteadyState_NotAllHandedOff(t *testing.T) {
	c := newTestController(t, argoServer(DefaultArgoNamespace, 1))
	c.phases[tierApps] = PhasePending
	status := newStatus()
	if idle := c.handleSteadyState(context.Background(), defaultCfg(), status); idle {
		t.Fatal("watchdog must not idle while a tier is still pending")
	}
}

// argoCDHealthy reflects the argocd-server Deployment availability.
func TestArgoCDHealthy(t *testing.T) {
	healthy := newTestController(t, argoServer(DefaultArgoNamespace, 1))
	if ok, _ := healthy.argoCDHealthy(context.Background(), defaultCfg()); !ok {
		t.Fatal("argocd-server with 1 available replica should be healthy")
	}
	unhealthy := newTestController(t, argoServer(DefaultArgoNamespace, 0))
	if ok, _ := unhealthy.argoCDHealthy(context.Background(), defaultCfg()); ok {
		t.Fatal("argocd-server with 0 available replicas should be unhealthy")
	}
	missing := newTestController(t)
	if ok, _ := missing.argoCDHealthy(context.Background(), defaultCfg()); ok {
		t.Fatal("missing argocd-server should be unhealthy")
	}
}

// nodesReady is the Cilium unblock predicate: every node Ready.
func TestNodesReady(t *testing.T) {
	ready := newTestController(t, node("n1", true), node("n2", true))
	if ok, err := ready.nodesReady(context.Background()); err != nil || !ok {
		t.Fatalf("all-ready nodes: ok=%v err=%v", ok, err)
	}
	mixed := newTestController(t, node("n1", true), node("n2", false))
	if ok, _ := mixed.nodesReady(context.Background()); ok {
		t.Fatal("a not-ready node must make nodesReady false")
	}
	none := newTestController(t)
	if ok, _ := none.nodesReady(context.Background()); ok {
		t.Fatal("zero nodes must be not-ready")
	}
}

// node builds a Node with a Ready condition set true/false.
func node(name string, ready bool) *corev1.Node {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: cond}},
		},
	}
}
