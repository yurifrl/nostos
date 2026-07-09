package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yurifrl/nostos/internal/cli/dryrun"
	"github.com/yurifrl/nostos/internal/cli/inputx"
	"github.com/yurifrl/nostos/internal/osimage"
	_ "github.com/yurifrl/nostos/internal/osimage/proxmox" // register proxmox OSImage
	_ "github.com/yurifrl/nostos/internal/osimage/talos"   // register talos OSImage
	"github.com/yurifrl/nostos/internal/registry"
)

func newRenderCmd() *cobra.Command {
	var (
		noValidate bool
		dryRun     bool
	)
	cmd := &cobra.Command{
		Use:   "render NODE",
		Short: "Render NODE's machineconfig with secrets injected",
		Args:  cobra.ExactArgs(1),
		RunE: runEFunc(func(cmd *cobra.Command, args []string) error {
			if err := inputx.ValidateNodeName(args[0]); err != nil {
				return err
			}
			if dryRun {
				plan := dryrun.New("render").
					Add("load", "load config.yaml + node "+args[0]).
					Add("inject", "inject secrets via op:// references")
				if !noValidate {
					plan.AddArgv("validate", "talosctl validate", []string{"talosctl", "validate", "--config", "<rendered>"}, nil)
				}
				plan.Add("write", "write rendered machineconfig under state/configs/")
				return emitDryRun(plan)
			}
			cfg, p, err := loadConfig()
			if err != nil {
				return err
			}
			img, err := osimage.For(osimage.Deps{Cfg: cfg, Paths: p}, args[0])
			if err != nil {
				return err
			}
			data, err := img.NodeConfig(cmd.Context(), args[0], !noValidate)
			if err != nil {
				return err
			}
			if data == nil {
				// This OS installs no machineconfig (e.g. proxmox).
				if outputMode == "json" {
					return outputJSON(map[string]any{"status": "noop", "node": args[0], "os": img.Name(), "reason": "os installs no machineconfig"})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s installs no machineconfig; nothing to render for %s\n", img.Name(), args[0])
				return nil
			}
			out := registry.ConfigPath(p, cfg.Nodes[args[0]], args[0])
			if outputMode == "json" {
				return outputJSON(map[string]any{"status": "rendered", "path": out, "node": args[0]})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Rendered %s\n", out)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&noValidate, "no-validate", false, "skip talosctl validate on output")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview planned actions as JSON; no subprocesses spawned")
	return cmd
}
