package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// newHostsCmd manages the local /etc/hosts entry for the cluster endpoint
// (e.g. api.k8s.lan) so tools on this machine can resolve it to the control
// planes. nostos injects the same aliases into the nodes' host entries; this
// mirrors them locally. Idempotent: rewrites a marker-delimited managed block
// (update-if-exists) or appends it (if absent).
func newHostsCmd() *cobra.Command {
	var (
		dryRun    bool
		hostsPath string
	)
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "Update /etc/hosts so the cluster endpoint resolves to the control planes",
		Long: "Resolve the cluster endpoint hostname (e.g. api.k8s.lan) to every\n" +
			"control-plane address (LAN + Tailscale) by writing a nostos-managed\n" +
			"block to /etc/hosts. Re-run any time roles change; it updates the\n" +
			"existing block in place or appends it. Writing /etc/hosts needs root.",
		Args: cobra.NoArgs,
		RunE: runEFuncSimple(func(cmd *cobra.Command, args []string) error {
			cfg, _, err := loadConfig()
			if err != nil {
				return err
			}
			host, err := cfg.Cluster.EndpointHost()
			if err != nil {
				return err
			}
			addrs := cfg.ControlPlaneEndpointAddrs()
			if len(addrs) == 0 {
				return fmt.Errorf("no controlplane addresses in config")
			}
			if hostsPath == "" {
				hostsPath = "/etc/hosts"
			}

			existing, err := os.ReadFile(hostsPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", hostsPath, err)
			}
			updated := rewriteManagedHosts(string(existing), host, addrs)

			if outputMode == "json" {
				return outputJSON(map[string]any{
					"file": hostsPath, "host": host, "addrs": addrs,
					"changed": updated != string(existing), "dry_run": dryRun,
				})
			}

			if updated == string(existing) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already up to date (%s -> %s)\n",
					hostsPath, host, strings.Join(addrs, ", "))
				return nil
			}
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "would write %s:\n%s\n", hostsPath,
					managedBlock(host, addrs))
				return nil
			}
			if err := os.WriteFile(hostsPath, []byte(updated), 0o644); err != nil {
				if os.IsPermission(err) {
					return fmt.Errorf("writing %s needs root; re-run with sudo:\n"+
						"  sudo go run ./.submodules/nostos/cmd/nostos --config nostos/config.yaml hosts", hostsPath)
				}
				return fmt.Errorf("write %s: %w", hostsPath, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated %s: %s -> %s\n",
				hostsPath, host, strings.Join(addrs, ", "))
			return nil
		}),
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the managed block without writing")
	cmd.Flags().StringVar(&hostsPath, "file", "/etc/hosts", "hosts file path")
	return cmd
}

// managedBlock renders the marker-delimited block mapping host to each addr.
func managedBlock(host string, addrs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# BEGIN nostos-managed: %s\n", host)
	for _, a := range addrs {
		fmt.Fprintf(&b, "%s\t%s\n", a, host)
	}
	fmt.Fprintf(&b, "# END nostos-managed: %s\n", host)
	return b.String()
}

// rewriteManagedHosts strips any prior managed block for host and any stray
// line mapping host, then appends a fresh managed block.
func rewriteManagedHosts(existing, host string, addrs []string) string {
	begin := "# BEGIN nostos-managed: " + host
	end := "# END nostos-managed: " + host
	var kept []string
	inBlock := false
	for _, line := range strings.Split(existing, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == begin {
			inBlock = true
			continue
		}
		if inBlock {
			if trimmed == end {
				inBlock = false
			}
			continue
		}
		if hostLineMaps(line, host) {
			continue
		}
		kept = append(kept, line)
	}
	body := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	return body + "\n\n" + managedBlock(host, addrs)
}

// hostLineMaps reports whether a non-comment hosts line lists host as a name.
func hostLineMaps(line, host string) bool {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return false
	}
	fields := strings.Fields(s)
	for _, f := range fields[1:] {
		if f == host {
			return true
		}
	}
	return false
}
