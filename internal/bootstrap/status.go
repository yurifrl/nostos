package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// TierState is the lifecycle state of one bootstrap tier (design §7).
type TierState string

const (
	TierPending  TierState = "Pending"
	TierApplying TierState = "Applying"
	TierReady    TierState = "Ready"
	TierFailed   TierState = "Failed"
)

// TierStatus is the rolled-up state of a single tier.
type TierStatus struct {
	State     TierState `json:"state"`
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (t *TierStatus) set(state TierState, msg string) {
	t.State = state
	t.Message = msg
	t.UpdatedAt = time.Now().UTC()
}

// Status is the controller's single-pane-of-glass status (design §7). It is
// serialized into the nostos-bootstrap-status ConfigMap rather than a CRD to
// keep the scaffold simple (design open-decision §9).
type Status struct {
	Cilium        TierStatus `json:"cilium"`
	ESO           TierStatus `json:"eso"`
	ArgoCD        TierStatus `json:"argocd"`
	Apps          TierStatus `json:"apps"`
	LastError     string     `json:"lastError,omitempty"`
	LastReconcile time.Time  `json:"lastReconcile"`
}

// newStatus seeds every tier as Pending.
func newStatus() *Status {
	now := time.Now().UTC()
	pending := TierStatus{State: TierPending, UpdatedAt: now}
	return &Status{Cilium: pending, ESO: pending, ArgoCD: pending, Apps: pending}
}

// allReady reports whether every tier reached Ready.
func (s *Status) allReady() bool {
	return s.Cilium.State == TierReady &&
		s.ESO.State == TierReady &&
		s.ArgoCD.State == TierReady &&
		s.Apps.State == TierReady
}

// writeStatus upserts the status ConfigMap via Server-Side Apply. The status
// JSON is the canonical surface; per-tier string keys are added for quick
// `kubectl get cm nostos-bootstrap-status -o yaml` reads.
func writeStatus(ctx context.Context, kube kubernetes.Interface, namespace string, s *Status) error {
	blob, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	data := map[string]string{
		"status.json":   string(blob),
		"cilium":        string(s.Cilium.State),
		"eso":           string(s.ESO.State),
		"argocd":        string(s.ArgoCD.State),
		"apps":          string(s.Apps.State),
		"lastError":     s.LastError,
		"lastReconcile": s.LastReconcile.Format(time.RFC3339),
	}

	cm := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      StatusConfigMapName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "nostos-bootstrap",
				"app.kubernetes.io/managed-by": FieldManager,
			},
		},
		Data: data,
	}

	// Use Apply (SSA) so repeated reconciles don't fight over the object. Fall
	// back to create on first run is unnecessary: Apply upserts.
	payload, err := json.Marshal(cm)
	if err != nil {
		return fmt.Errorf("marshal status configmap: %w", err)
	}
	_, err = kube.CoreV1().ConfigMaps(namespace).Patch(
		ctx,
		StatusConfigMapName,
		types.ApplyPatchType,
		payload,
		metav1.PatchOptions{FieldManager: FieldManager, Force: ptrBool(true)},
	)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("apply status configmap: %w", err)
	}
	return nil
}

func ptrBool(b bool) *bool { return &b }
