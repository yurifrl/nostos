// Package upgrade — Kubernetes control-plane upgrade helpers for
// `nostos upgrade-k8s`. Mirrors the flow of a manual multi-hop upgrade:
// detect the lowest running control-plane version, hop minor by minor with
// `talosctl upgrade-k8s`, gate each hop on the live pod spec images, then
// sync the kubelet pin in the machineconfig templates (git stays the source
// of truth — no in-place kubelet patching that would drift from config).
package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// kubeImageRE matches the control-plane images we track, capturing the
// component and tag. The registry varies (registry.k8s.io on Talos static
// pods, ghcr.io/siderolabs elsewhere), so match on the image basename.
var kubeImageRE = regexp.MustCompile(`(?:^|/)kube-(apiserver|controller-manager|scheduler|proxy):v(\d+\.\d+\.\d+)`)

// kubeletTagRE extracts `vX.Y.Z` from a kubelet image or kubeletVersion field.
var kubeletTagRE = regexp.MustCompile(`kubelet:v(\d+\.\d+\.\d+)`)

// K8sState is the cluster's live Kubernetes version picture.
type K8sState struct {
	// Floor is the LOWEST control-plane version (apiserver, controller-manager,
	// scheduler, kube-proxy) — the version every hop must pass through.
	Floor Version
	// Components records each tracked component's version (node/component →
	// version) for reporting which pod lags behind.
	Components map[string]Version
	// Kubelets maps node name → kubelet version (from nodeInfo.kubeletVersion).
	Kubelets map[string]Version
	// NotReady lists node names whose Ready condition is not True.
	NotReady []string
}

// kubectlJSON runs a kubectl query against the admin kubeconfig and returns
// the parsed JSON body. kubectl is invoked directly (no talosctl shim) so the
// gate reads the same API an operator would.
func kubectlJSON(ctx context.Context, kubeconfig, args string, dst any) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not found on PATH (required for k8s floor detection)")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	argv := append([]string{"--kubeconfig", kubeconfig}, strings.Fields(args)...)
	argv = append(argv, "-o", "json")
	out, err := exec.CommandContext(ctx, "kubectl", argv...).Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = strings.TrimSpace(string(ee.Stderr))
		}
		return fmt.Errorf("kubectl %s: %v %s", args, err, detail)
	}
	return json.Unmarshal(out, dst)
}

// DetectK8sState reads the cluster's control-plane + kubelet versions:
// the pod images in kube-system (static control-plane pods + kube-proxy
// DaemonSet pods) and each node's kubeletVersion. Floor is the lowest
// tracked control-plane component.
func DetectK8sState(ctx context.Context, kubeconfig string) (K8sState, error) {
	st := K8sState{
		Components: map[string]Version{},
		Kubelets:   map[string]Version{},
	}

	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := kubectlJSON(ctx, kubeconfig, "get pods -n kube-system", &pods); err != nil {
		return st, err
	}
	for _, it := range pods.Items {
		for _, c := range it.Spec.Containers {
			m := kubeImageRE.FindStringSubmatch(c.Image)
			if m == nil {
				continue
			}
			v, err := ParseVersion("v" + m[2])
			if err != nil {
				return st, fmt.Errorf("parse image tag %q: %w", c.Image, err)
			}
			key := it.Metadata.Name + "/" + m[1]
			st.Components[key] = v
			if st.Floor == (Version{}) || v.Less(st.Floor) {
				st.Floor = v
			}
		}
	}

	var nodes struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := kubectlJSON(ctx, kubeconfig, "get nodes", &nodes); err != nil {
		return st, err
	}
	for _, it := range nodes.Items {
		if m := kubeletTagRE.FindStringSubmatch(it.Status.NodeInfo.KubeletVersion); m != nil {
			if v, err := ParseVersion("v" + m[1]); err == nil {
				st.Kubelets[it.Metadata.Name] = v
			}
		}
		ready := false
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
				break
			}
		}
		if !ready {
			st.NotReady = append(st.NotReady, it.Metadata.Name)
		}
	}
	return st, nil
}

// PlanK8sHops returns the minor-by-minor path from floor to target (e.g.
// v1.32.1 → v1.37.1 yields v1.33.0, v1.34.0, v1.35.0, v1.36.0, v1.37.1).
// Kubernetes control-plane components refuse to skip minors (apiserver only
// n-3), so every intermediate minor is a hop.
func PlanK8sHops(floor, target Version) ([]Version, error) {
	if target.Less(floor) {
		return nil, fmt.Errorf("target %s is below the running floor %s", target, floor)
	}
	if floor.Major != target.Major {
		return nil, fmt.Errorf("cross-major upgrade %s → %s is not supported", floor, target)
	}
	var hops []Version
	for minor := floor.Minor; minor <= target.Minor; minor++ {
		v := Version{Major: target.Major, Minor: minor}
		if v.Minor == target.Minor {
			v.Patch = target.Patch // final hop lands on the exact target patch
		}
		// Same-minor hop stays only when the target patch is ahead (covers
		// patch-only bumps like v1.33.0 → v1.33.1 and skips no-ops).
		if !floor.Less(v) {
			continue
		}
		hops = append(hops, v)
	}
	return hops, nil
}

// ExecUpgradeK8s runs `talosctl upgrade-k8s --to <version> --upgrade-kubelet=false`
// against one control-plane node. Kubelet is deliberately NOT patched here:
// the kubelet image pin lives in the machineconfig templates, so the final
// sync happens through render+apply (git stays the source of truth).
func ExecUpgradeK8s(bin, talosconfigPath, nodeIP string, to Version) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	args := []string{
		"--talosconfig", talosconfigPath,
		"--nodes", nodeIP, "--endpoints", nodeIP,
		"upgrade-k8s",
		"--to", to.String(),
		"--upgrade-kubelet=false",
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("talosctl upgrade-k8s (%s, to %s): %s", nodeIP, to, strings.TrimSpace(string(out)))
	}
	return nil
}

// WaitK8sHop polls the cluster until every tracked control-plane component
// (floor) has reached at least hop. progress receives friendly status lines.
func WaitK8sHop(ctx context.Context, kubeconfig string, hop Version, timeout, interval time.Duration, progress func(string)) error {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		st, err := DetectK8sState(ctx, kubeconfig)
		if err == nil {
			// No components found means the probe is broken, not healthy.
			if st.Floor != (Version{}) && !st.Floor.Less(hop) {
				return nil
			}
			last = st.Floor.String()
			if progress != nil {
				progress(fmt.Sprintf("  … control-plane floor at %s, waiting for %s", last, hop))
			}
		} else if progress != nil {
			progress(fmt.Sprintf("  … kubectl unreachable (%v), retrying", err))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for control-plane floor %s to reach %s", last, hop)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// KubeletImageTag returns the kubelet image tag (e.g. "v1.32.1") running on
// nodeIP, read from `talosctl containers` (the kubelet image line carries
// ghcr.io/siderolabs/kubelet:vX.Y.Z).
func KubeletImageTag(bin, talosconfigPath, nodeIP string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"--talosconfig", talosconfigPath, "--nodes", nodeIP, "--endpoints", nodeIP, "containers"}
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return "", fmt.Errorf("talosctl containers (%s): %v", nodeIP, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if idx := strings.Index(line, "kubelet:"); idx >= 0 {
			tag := strings.TrimSpace(line[idx+len("kubelet:"):])
			if tag != "" {
				return tag, nil
			}
		}
	}
	return "", nil
}

// WaitKubelet polls nodeIP until its kubelet image tag reaches want.
func WaitKubelet(ctx context.Context, bin, talosconfigPath, nodeIP string, want Version, timeout, interval time.Duration, progress func(string)) error {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		tag, err := KubeletImageTag(bin, talosconfigPath, nodeIP)
		if err == nil && tag != "" {
			if tag == want.String() {
				return nil
			}
			last = tag
			if progress != nil {
				progress(fmt.Sprintf("  … %s kubelet at %s, waiting for %s", nodeIP, last, want))
			}
		} else if progress != nil {
			progress(fmt.Sprintf("  … %s kubelet unreachable, retrying", nodeIP))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s kubelet to reach %s (last seen %s)", nodeIP, want, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// NodesReady reports the names of nodes whose Ready condition is not True
// (empty slice = every node Ready).
func NodesReady(ctx context.Context, kubeconfig string) ([]string, error) {
	st, err := DetectK8sState(ctx, kubeconfig)
	if err != nil {
		return nil, err
	}
	return st.NotReady, nil
}

var kubeletPinRE = regexp.MustCompile(`ghcr\.io/siderolabs/kubelet:v\d+\.\d+\.\d+`)

// SyncKubeletTemplate rewrites the kubelet image pin in one machineconfig
// template to the target version. It returns whether the template carries a
// kubelet pin at all and whether the file was changed.
func SyncKubeletTemplate(path string, target Version) (found, changed bool, err error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return false, false, err
	}
	repl := "ghcr.io/siderolabs/kubelet:" + target.String()
	out := kubeletPinRE.ReplaceAll(body, []byte(repl))
	if string(out) == string(body) {
		// No change: either already pinned at target, or no pin at all.
		return kubeletPinRE.Match(body), false, nil
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return true, false, err
	}
	return true, true, nil
}
