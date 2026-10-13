package upgrade

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// resolveTalosctl returns talosctlBin when non-empty, otherwise falls back to
// "talosctl" on PATH. It errors only when the fallback is not found.
func resolveTalosctl(talosctlBin string) (string, error) {
	if talosctlBin != "" {
		return talosctlBin, nil
	}
	if _, err := exec.LookPath("talosctl"); err != nil {
		return "", fmt.Errorf("talosctl not found on PATH")
	}
	return "talosctl", nil
}

// DetectVersion shells out to `talosctl version` against nodeIP and returns the
// running SERVER Talos version. talosctlBin selects the binary to exec; when
// empty it falls back to "talosctl" on PATH. It bounds the call with a context
// timeout.
func DetectVersion(talosctlBin, talosconfigPath, nodeIP string) (Version, error) {
	bin, err := resolveTalosctl(talosctlBin)
	if err != nil {
		return Version{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{
		"version",
		"--nodes", nodeIP,
		"--endpoints", nodeIP,
		"--talosconfig", talosconfigPath,
		"--short",
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return Version{}, fmt.Errorf("talosctl version (%s): %s", nodeIP, strings.TrimSpace(string(out)))
	}
	return parseServerVersion(string(out))
}

// DetectVersionAny tries each candidate endpoint in order and returns the
// first successful SERVER version plus the endpoint that worked. Workers that
// register on the tailnet but whose LAN IP is not routable from the operator
// machine (e.g. an offsite Raspberry Pi) are detected via their Tailscale IP.
func DetectVersionAny(talosctlBin, talosconfigPath string, endpoints ...string) (Version, string, error) {
	var last error
	for _, ep := range endpoints {
		if ep == "" {
			continue
		}
		v, err := DetectVersion(talosctlBin, talosconfigPath, ep)
		if err == nil {
			return v, ep, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no endpoints provided")
	}
	return Version{}, "", last
}

// Reboot issues `talosctl reboot` against a running node and waits for it to
// come back (talosctl's default action semantics). Bounded at 15 minutes:
// slow SBCs can take several minutes to stop all pods and boot.
func Reboot(ctx context.Context, talosctlBin, talosconfigPath, nodeIP string) error {
	bin, err := resolveTalosctl(talosctlBin)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	args := []string{
		"reboot",
		"--nodes", nodeIP,
		"--endpoints", nodeIP,
		"--talosconfig", talosconfigPath,
	}
	out, err := exec.CommandContext(rctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("talosctl reboot (%s): %s", nodeIP, strings.TrimSpace(string(out)))
	}
	return nil
}

// isStagedInstall reports whether talosctl upgrade output shows the installer
// finished writing the new image but the node never rebooted. This happens on
// worker nodes: talosctl drains the node pre-reboot by fetching kubeconfig
// FROM THE TARGET NODE, and workers refuse (kubeconfig is control-plane-only),
// so the client aborts after the install, before the reboot.
func isStagedInstall(out string) bool {
	return strings.Contains(out, "installation of") &&
		strings.Contains(out, "complete") &&
		strings.Contains(out, "error creating Kubernetes client for drain")
}

// parseServerVersion extracts the SERVER Talos version from `talosctl version`
// output (handles both --short "Server:\n\tTag: v1.10.3" forms and a single
// "Server: v1.10.3" line). Pure helper for testability.
func parseServerVersion(out string) (Version, error) {
	lines := strings.Split(out, "\n")
	inServer := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Server:"):
			inServer = true
			// "Server: v1.10.3" single-line form.
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Server:"))
			if rest != "" {
				if v, err := ParseVersion(rest); err == nil {
					return v, nil
				}
			}
		case strings.HasPrefix(trimmed, "Client:"):
			inServer = false
		case inServer && strings.HasPrefix(trimmed, "Tag:"):
			tag := strings.TrimSpace(strings.TrimPrefix(trimmed, "Tag:"))
			return ParseVersion(tag)
		}
	}
	return Version{}, fmt.Errorf("could not find server version in talosctl output")
}

// Upgrade runs `talosctl upgrade` against a running node, pulling the given
// installer image. The image path uses /installer/ (running-node upgrades),
// distinct from the /metal-installer/ path used at first install time.
func Upgrade(ctx context.Context, talosctlBin, talosconfigPath, nodeIP, image string) error {
	bin, err := resolveTalosctl(talosctlBin)
	if err != nil {
		return err
	}
	args := []string{
		"upgrade",
		"--nodes", nodeIP,
		"--endpoints", nodeIP,
		"--talosconfig", talosconfigPath,
		"--image", image,
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		// Workers: talosctl aborts pre-reboot because it cannot fetch
		// kubeconfig from a worker node. The install itself completed; finish
		// the upgrade by rebooting directly. The WaitHealthy gate in the
		// caller then verifies the node actually came back at `want`.
		if isStagedInstall(string(out)) {
			if rerr := Reboot(ctx, talosctlBin, talosconfigPath, nodeIP); rerr != nil {
				return fmt.Errorf(
					"talosctl upgrade (%s): install of new image completed but the node did not reboot "+
						"(talosctl's pre-reboot drain cannot fetch kubeconfig from workers); "+
						"automatic reboot failed: %v; original output: %s",
					nodeIP, rerr, strings.TrimSpace(string(out)))
			}
			return nil
		}
		return fmt.Errorf("talosctl upgrade (%s): %s", nodeIP, strings.TrimSpace(string(out)))
	}
	return nil
}

// InstallerImage builds the running-node upgrade installer image reference.
func InstallerImage(schematic string, v Version) string {
	return fmt.Sprintf("factory.talos.dev/installer/%s:%s", schematic, v.String())
}

// WaitHealthy polls DetectVersion until the node reports want (and is
// reachable), AND the kubelet service is Running+OK — today the version probe
// alone can pass while the old kubelet is wedged in SIGTERM (observed on an
// RK1: install complete, node answering talos API, kubelet stuck "Stopping").
// progress is called with friendly status lines; it may be nil.
func WaitHealthy(talosctlBin, talosconfigPath, nodeIP string, want Version, timeout, interval time.Duration, progress func(string)) error {
	deadline := time.Now().Add(timeout)
	for {
		got, err := DetectVersion(talosctlBin, talosconfigPath, nodeIP)
		if err == nil && got == want {
			healthy, detail := KubeletHealthy(talosctlBin, talosconfigPath, nodeIP)
			if healthy {
				if progress != nil {
					progress(fmt.Sprintf("✓ %s is healthy at %s (kubelet OK)", nodeIP, want))
				}
				return nil
			}
			if progress != nil {
				progress(fmt.Sprintf("  … %s at %s but kubelet not healthy (%s), waiting", nodeIP, got, detail))
			}
		} else if progress != nil {
			if err != nil {
				progress(fmt.Sprintf("  … %s not reachable yet, retrying", nodeIP))
			} else {
				progress(fmt.Sprintf("  … %s at %s, waiting for %s", nodeIP, got, want))
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("timeout waiting for %s to reach %s: %v", nodeIP, want, err)
			}
			return fmt.Errorf("timeout waiting for %s to reach %s (last seen %s)", nodeIP, want, got)
		}
		time.Sleep(interval)
	}
}

// KubeletHealthy reports whether the kubelet service on nodeIP is in the
// Running state with a passing health check. detail carries the raw
// state/health when unhealthy.
func KubeletHealthy(talosctlBin, talosconfigPath, nodeIP string) (bool, string) {
	bin, err := resolveTalosctl(talosctlBin)
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{
		"service", "kubelet",
		"--nodes", nodeIP,
		"--endpoints", nodeIP,
		"--talosconfig", talosconfigPath,
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return false, strings.TrimSpace(string(out))
	}
	state, health := parseServiceStatus(string(out))
	return state == "Running" && health == "Ok", state + "/" + health
}

// parseServiceStatus pulls STATE and HEALTH values from `talosctl service`
// output. Health is normalized to title case ("OK" → "Ok").
func parseServiceStatus(out string) (state, health string) {
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "STATE"):
			state = strings.TrimSpace(strings.TrimPrefix(trimmed, "STATE"))
		case strings.HasPrefix(trimmed, "HEALTH"):
			health = strings.TrimSpace(strings.TrimPrefix(trimmed, "HEALTH"))
		}
	}
	// talosctl prints "OK"; normalize to "Ok" for a case-insensitive compare.
	switch strings.ToLower(health) {
	case "ok":
		health = "Ok"
	}
	return state, health
}
