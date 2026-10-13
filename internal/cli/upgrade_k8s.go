package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/yurifrl/nostos/internal/cli/errs"
	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/registry"
	"github.com/yurifrl/nostos/internal/upgrade"
)

func newUpgradeK8sCmd() *cobra.Command {
	var (
		to     string
		yes    bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "upgrade-k8s --to vX.Y.Z",
		Short: "Rolling Kubernetes control-plane upgrade (minor-by-minor hops, gated, kubelet via templates)",
		Long: "Upgrade the Kubernetes control plane safely. Detects the lowest\n" +
			"running version (apiserver / controller-manager / scheduler /\n" +
			"kube-proxy / kubelet), plans one hop per intermediate minor\n" +
			"(apiserver refuses to skip minors), runs `talosctl upgrade-k8s` on\n" +
			"each reachable control-plane node per hop, and gates every hop on\n" +
			"the live pod images before continuing.\n" +
			"The kubelet pin is NOT patched in place: templates are bumped and\n" +
			"machineconfigs re-rendered + applied (no-reboot), keeping git as\n" +
			"the source of truth.",
		Args: cobra.NoArgs,
		RunE: runEFunc(func(cmd *cobra.Command, args []string) error {
			cfg, p, err := loadConfig()
			if err != nil {
				return err
			}
			if to == "" {
				return errs.Validation("E_MISSING_TO",
					"--to is required").
					WithHint("nostos upgrade-k8s --to v1.37.1 --dry-run")
			}
			target, err := upgrade.ParseVersion(to)
			if err != nil {
				return errs.Validation("E_INVALID_VERSION",
					fmt.Sprintf("invalid --to version %q: %v", to, err)).
					WithHint("use a Kubernetes version like v1.37.1")
			}
			kubeconfig := p.Kubeconfig()
			if _, err := os.Stat(kubeconfig); err != nil {
				return errs.NotFound("E_NO_KUBECONFIG",
					fmt.Sprintf("admin kubeconfig not found at %s", kubeconfig)).
					WithHint("run `nostos kubeconfig` first")
			}

			ctx := cmd.Context()
			state, err := upgrade.DetectK8sState(ctx, kubeconfig)
			if err != nil {
				return errs.Network("E_K8S_STATE", err.Error()).
					WithHint("check cluster reachability (kubectl --kubeconfig " + kubeconfig + " get nodes)")
			}
			hops, err := upgrade.PlanK8sHops(state.Floor, target)
			if err != nil {
				return errs.Validation("E_PATH", err.Error())
			}

			// Reachable control-plane nodes get upgrade-k8s runs; every
			// registered Talos node gets the kubelet template sync. Endpoint
			// fallback mirrors `nostos upgrade` (LAN IP → Tailscale IP).
			type nodePlan struct {
				name  string
				node  config.Node
				ip    string // endpoint that actually answered
				cur   upgrade.Version
				reach bool
			}
			var cps []nodePlan
			var all []nodePlan
			for _, e := range registry.List(cfg) {
				n := e.Node
				if n.NodeType() == "host" {
					continue
				}
				np := nodePlan{name: e.Name, node: n}
				v, ep, err := upgrade.DetectVersionAny("", p.Talosconfig(), append([]string{n.IP}, n.TailscaleIP)...)
				if err == nil {
					np.reach = true
					np.ip = ep
					np.cur = v
				}
				all = append(all, np)
				if n.Role == "controlplane" {
					cps = append(cps, np)
				}
			}
			sort.Slice(cps, func(i, j int) bool { return cps[i].name < cps[j].name })
			sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
			if len(cps) == 0 {
				return errs.Validation("E_NO_CONTROLPLANE",
					"no controlplane nodes in config.yaml").
					WithHint("nostos node list")
			}
			var reachableCPs []nodePlan
			for _, c := range cps {
				if c.reach {
					reachableCPs = append(reachableCPs, c)
				}
			}
			if len(reachableCPs) == 0 {
				return errs.Network("E_CP_UNREACHABLE",
					"no control-plane node is reachable; cannot run upgrade-k8s").
					WithHint("check tailscale / LAN connectivity, then re-run")
			}

			// Kubelet template plan: which templates need a bump to reach the
			// target pin (node-level, independent of the control-plane hops).
			type kubeletPlan struct {
				name     string
				node     config.Node
				template string
				cur      upgrade.Version // detected Talos version (0 = unreachable)
				ip       string          // endpoint that answered detection
				from     string          // current kubelet version, "—" when unknown
				reach    bool
				dirty    bool // kubelet currently below target
			}
			var kplans []kubeletPlan
			for _, np := range all {
				tmpl := filepath.Join(p.Templates(), np.node.Template)
				kub := upgrade.Version{}
				if v, ok := state.Kubelets[np.name]; ok {
					kub = v
				}
				from := "—"
				if kub != (upgrade.Version{}) {
					from = kub.String()
				}
				kplans = append(kplans, kubeletPlan{
					name:     np.name,
					node:     np.node,
					template: tmpl,
					cur:      np.cur,
					ip:       np.ip,
					from:     from,
					reach:    np.reach,
					dirty:    kub != (upgrade.Version{}) && kub.Less(target),
				})
			}

			preview := map[string]any{
				"status":   "preview",
				"floor":    state.Floor.String(),
				"target":   target.String(),
				"hops":     nil,
				"kubelets": nil,
			}
			if len(hops) > 0 {
				var hs []string
				for _, h := range hops {
					hs = append(hs, h.String())
				}
				preview["hops"] = hs
			}
			var knames []string
			for _, kp := range kplans {
				if kp.dirty {
					knames = append(knames, kp.name)
				}
			}
			preview["kubelet_bump"] = knames

			if dryRun {
				if outputMode == "json" {
					return outputJSON(preview)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "k8s state: floor %s, target %s\n", state.Floor, target)
				fmt.Fprintf(cmd.OutOrStdout(), "control-plane hops:\n")
				for _, h := range hops {
					var cnames []string
					for _, c := range reachableCPs {
						cnames = append(cnames, c.name)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  hop %s: %v\n", h, cnames)
				}
				if len(hops) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  (none — control plane already at %s)\n", state.Floor)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "kubelet template sync (render + apply no-reboot):\n")
				for _, kp := range kplans {
					verb := "up-to-date"
					if kp.dirty {
						verb = fmt.Sprintf("%s → %s", kp.from, target)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %-12s %s (%s)\n", kp.name, verb, kp.template)
				}
				if len(state.NotReady) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "note: nodes not Ready: %v\n", state.NotReady)
				}
				return nil
			}

			// Mutating path — require --yes.
			if !yes {
				return errs.Conflict("E_CONFIRM_REQUIRED",
					"upgrade-k8s rewrites control-plane manifests and machineconfigs; refusing without --yes").
					WithDetails(preview).
					WithHint("review with --dry-run, then re-run with --yes")
			}

			// Hops: run upgrade-k8s on each reachable control-plane node, then
			// gate the whole cluster floor before the next hop. Each hop uses
			// a talosctl client matching the node's current Talos version
			// (same rationale as the OS upgrade: never a much-newer client on
			// an older server).
			binCache := filepath.Join(p.Cache(), "bin")
			if err := os.MkdirAll(binCache, 0o755); err != nil {
				return errs.FromGo(err)
			}
			progress := func(line string) {
				if outputMode != "json" {
					fmt.Fprintln(cmd.OutOrStdout(), line)
				}
			}
			for _, h := range hops {
				for _, c := range reachableCPs {
					talosctlBin, err := upgrade.EnsureTalosctl(ctx, c.cur, binCache, nil)
					if err != nil {
						return errs.Network("E_TALOSCTL",
							fmt.Sprintf("ensure talosctl %s for %s: %v", c.cur, c.name, err)).
							WithHint("check network access to github.com/siderolabs/talos releases")
					}
					if outputMode != "json" {
						fmt.Fprintf(cmd.OutOrStdout(), "→ hop %s: upgrade-k8s on %s (%s)\n", h, c.name, c.ip)
					}
					if err := upgrade.ExecUpgradeK8s(talosctlBin, p.Talosconfig(), c.ip, h); err != nil {
						return errs.FromGo(err)
					}
				}
				if err := upgrade.WaitK8sHop(ctx, kubeconfig, h, 10*time.Minute, 15*time.Second, progress); err != nil {
					return errs.Timeout("E_HEALTH", err.Error()).
						WithHint("control-plane did not reach the hop version in time")
				}
			}

			// Kubelet sync: bump templates, re-render, apply no-reboot, gate.
			for _, kp := range kplans {
				if !kp.dirty {
					continue
				}
				found, changed, err := upgrade.SyncKubeletTemplate(kp.template, target)
				if err != nil {
					return errs.FromGo(err)
				}
				if !found {
					if outputMode != "json" {
						fmt.Fprintf(cmd.OutOrStdout(), "warn: %s has no kubelet pin in template; skipping\n", kp.name)
					}
					continue
				}
				if !changed {
					continue // already pinned at target
				}
				if outputMode != "json" {
					fmt.Fprintf(cmd.OutOrStdout(), "→ kubelet %s: template → %s\n", kp.name, target)
				}
				out, err := registry.Render(cfg, p, kp.name, true)
				if err != nil {
					return errs.FromGo(err)
				}
				if !kp.reach || kp.cur == (upgrade.Version{}) {
					if outputMode != "json" {
						fmt.Fprintf(cmd.OutOrStdout(), "  warn: %s unreachable — template updated, apply pending\n", kp.name)
					}
					continue
				}
				// Apply to the endpoint that answered (LAN IP may be stale).
				np := kp.node
				np.IP = kp.ip
				if err := registry.Apply(p, np, out, "no-reboot"); err != nil {
					return errs.FromGo(err)
				}
				talosctlBin, err := upgrade.EnsureTalosctl(ctx, kp.cur, binCache, nil)
				if err != nil {
					return errs.FromGo(err)
				}
				if err := upgrade.WaitKubelet(ctx, talosctlBin, p.Talosconfig(), np.IP, target, 5*time.Minute, 10*time.Second, progress); err != nil {
					return errs.Timeout("E_KUBELET", err.Error()).
						WithHint("kubelet did not pick up the new image pin in time")
				}
			}

			// Final gates: every node Ready, floor at target.
			if notReady, err := upgrade.NodesReady(ctx, kubeconfig); err != nil {
				return errs.FromGo(err)
			} else if len(notReady) > 0 {
				return errs.Internal("E_NODES_NOT_READY",
					fmt.Sprintf("nodes not Ready after upgrade: %v", notReady)).
					WithHint("inspect kubelet/apid on the listed nodes")
			}

			if outputMode == "json" {
				return outputJSON(map[string]any{
					"status":    "upgraded",
					"target":    target.String(),
					"hops":      preview["hops"],
					"kubelet":   knames,
					"not_ready": state.NotReady,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Kubernetes control plane upgraded to %s (kubelet synced via templates)\n", target)
			return nil
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "target Kubernetes version (e.g. v1.37.1)")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the upgrade (required: rewrites manifests + machineconfigs)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the hop plan and kubelet template changes; no changes")
	return cmd
}
