package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yurifrl/nostos/internal/cli/dryrun"
	"github.com/yurifrl/nostos/internal/cli/errs"
	"github.com/yurifrl/nostos/internal/cli/inputx"
	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/image"
	"github.com/yurifrl/nostos/internal/osimage"
	"github.com/yurifrl/nostos/internal/paths"
	"github.com/yurifrl/nostos/internal/registry"
)

func newFlashCmd() *cobra.Command {
	var (
		outPath  string
		device   string
		compress bool
		dryRun   bool
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "flash NODE",
		Short: "Build a flashable Talos disk image for NODE (mints Tailscale key, renders config)",
		Long: "Produce a flashable disk image for the named node:\n" +
			"\n" +
			"  - Downloads (or reuses) the Talos raw image for the node's\n" +
			"    schematic + arch.\n" +
			"  - Renders the machineconfig (resolving secrets, minting a fresh\n" +
			"    Tailscale auth key embedded in the Tailscale extension).\n" +
			"  - Writes the image to --out FILE (optionally xz-compressed)\n" +
			"    or directly to --device /dev/diskN.\n" +
			"  - For RPi nodes, also emits an EEPROM recovery directory the\n" +
			"    operator can copy to a FAT32 SD card to enable network boot\n" +
			"    on a fresh Pi 4.\n" +
			"\n" +
			"After flashing, boot the node and apply the config with:\n" +
			"  nostos apply <node>",
		Args: cobra.ExactArgs(1),
		RunE: runEFunc(func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := inputx.ValidateNodeName(name); err != nil {
				return err
			}
			if outPath == "" && device == "" {
				return errs.Validation("E_FLASH_OUTPUT_REQUIRED",
					"either --out FILE or --device /dev/diskN is required")
			}
			if outPath != "" && device != "" {
				return errs.Validation("E_FLASH_OUTPUT_CONFLICT",
					"--out and --device are mutually exclusive")
			}

			cfg, p, err := loadConfig()
			if err != nil {
				return err
			}
			node, err := registry.Get(cfg, name)
			if err != nil {
				return errs.NotFound("E_NODE_NOT_FOUND", err.Error()).
					WithDetails(map[string]any{"name": name}).
					WithHint("nostos node list")
			}

			if dryRun {
				return emitFlashDryRun(cfg, node, name, outPath, device, compress)
			}

			// Confirmation gate when writing to a device.
			if device != "" && !yes {
				return errs.Conflict("E_CONFIRM_REQUIRED",
					fmt.Sprintf("writing to %s will overwrite the device; refusing without --yes", device)).
					WithDetails(map[string]any{"device": device, "node": name}).
					WithHint("re-run with --yes once you've confirmed the device path")
			}

			return runFlash(cmd.Context(), cfg, p, node, name, outPath, device, compress)
		}),
	}
	cmd.Flags().StringVar(&outPath, "out", "", "write image to FILE (.raw or .raw.xz). Sidecar <FILE>-config.yaml is also written.")
	cmd.Flags().StringVar(&device, "device", "", "write image directly to block device (e.g. /dev/disk10). Mutually exclusive with --out.")
	cmd.Flags().BoolVar(&compress, "compress", false, "xz-compress --output (no-op for --device)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview planned actions as JSON; no subprocesses, no key minting")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation when writing to a device")
	return cmd
}

func emitFlashDryRun(cfg *config.Config, node config.Node, name, outPath, device string, compress bool) error {
	plan := dryrun.New("flash")
	osName := node.OSName()
	plan.Add("preflight", fmt.Sprintf("validate node %s (os=%s, arch=%s)", name, osName, node.Arch))
	plan.Add("resolve", fmt.Sprintf("resolve %s image for %s", osName, name))
	plan.Add("download.image", fmt.Sprintf("download + cache the %s image", osName))
	if outPath != "" {
		extra := ""
		if compress {
			extra = " (xz-compressed)"
		}
		plan.Add("write.file", fmt.Sprintf("write main image to %s%s + any sidecars", outPath, extra))
	}
	if device != "" {
		plan.AddArgv("write.device", "write image to "+device,
			[]string{"dd", "if=<image>", "of=" + device, "bs=4M", "status=progress"}, nil)
	}
	plan.Add("instructions", "print OS-specific next-step notes")
	return emitDryRun(plan)
}

func runFlash(ctx context.Context, cfg *config.Config, p paths.Paths, node config.Node, name, outPath, device string, compress bool) error {
	if err := p.EnsureState(); err != nil {
		return err
	}

	// Select the OS and produce its flash plan via the osimage seam. No
	// per-OS branching here: talos yields a raw image + machineconfig sidecar
	// (+ RPi EEPROM note); proxmox yields a single ISO.
	img, err := osimage.For(osimage.Deps{Cfg: cfg, Paths: p}, name)
	if err != nil {
		return errs.FromGo(err)
	}
	// Preflight the device BEFORE the (slow) image download, so a
	// missing-device error surfaces quickly. If the device exists but isn't
	// writable, we'll use sudo dd for just the write phase.
	var deviceNeedsSudo bool
	if device != "" {
		var directWrite bool
		directWrite, err = checkDeviceWritable(device)
		if err != nil {
			return err
		}
		deviceNeedsSudo = !directWrite
	}
	if outputMode != "json" {
		fmt.Fprintf(outWriter, "→ resolving %s image for %s…\n", img.Name(), name)
	}
	ref, err := img.Resolve(ctx, name)
	if err != nil {
		return errs.FromGo(err)
	}
	if outputMode != "json" {
		fmt.Fprintf(outWriter, "→ preparing %s %s flash plan…\n", img.Name(), ref.Version)
	}
	plan, err := img.FlashPlan(ctx, name, ref)
	if err != nil {
		return errs.FromGo(err)
	}

	var res *image.Result
	if device != "" && deviceNeedsSudo {
		// Device not writable by current user — use sudo dd for just the
		// write. This allows the rest of the command (including 1Password
		// secret resolution) to run unprivileged.
		if outputMode != "json" {
			fmt.Fprintf(outWriter, "→ writing image to %s (via sudo)…\n", device)
		}
		res, err = writePlanToDeviceSudo(ctx, plan, device)
		if err != nil {
			return errs.FromGo(err)
		}
	} else {
		dest := image.Dest{Compress: compress}
		if device != "" {
			dest.Mode, dest.Path = image.ModeDevice, device
		} else {
			dest.Mode, dest.Path = image.ModeFile, outPath
		}
		if outputMode != "json" {
			fmt.Fprintf(outWriter, "→ writing image to %s…\n", dest.Path)
		}
		res, err = image.Write(ctx, plan, dest)
		if err != nil {
			return errs.FromGo(err)
		}
	}

	if outputMode == "json" {
		return outputJSON(map[string]any{
			"status":   "flashed",
			"node":     name,
			"os":       img.Name(),
			"version":  ref.Version,
			"image":    res.ImagePath,
			"sidecars": res.Sidecars,
		})
	}

	fmt.Fprintf(outWriter, "✓ image:  %s (%s -> %s)\n",
		res.ImagePath, humanBytes(res.BytesIn), humanBytes(res.BytesOut))
	for _, sc := range res.Sidecars {
		fmt.Fprintf(outWriter, "✓ sidecar: %s\n", sc)
	}
	fmt.Fprintln(outWriter)
	fmt.Fprintln(outWriter, "next steps:")
	if device != "" {
		fmt.Fprintf(outWriter, "  1. eject the device: %s\n", ejectHint(device))
		fmt.Fprintf(outWriter, "  2. plug it into the target node, power on\n")
	} else {
		fmt.Fprintf(outWriter, "  1. flash %s to the target disk:\n", filepath.Base(res.ImagePath))
		if compress {
			fmt.Fprintf(outWriter, "       xzcat %s | sudo dd of=/dev/rdiskN bs=4M status=progress\n", res.ImagePath)
		} else {
			fmt.Fprintf(outWriter, "       sudo dd if=%s of=/dev/rdiskN bs=4M status=progress\n", res.ImagePath)
		}
		fmt.Fprintf(outWriter, "  2. boot the node\n")
	}
	for _, note := range plan.Notes {
		fmt.Fprintf(outWriter, "  - %s\n", note)
	}
	return nil
}

// checkDeviceWritable verifies the operator can open the block device for
// writing, BEFORE any long download. Returns true if the device is directly
// writable, false if sudo will be needed for the write phase.
func checkDeviceWritable(device string) (directWrite bool, err error) {
	f, err := os.OpenFile(device, os.O_WRONLY, 0)
	if err == nil {
		_ = f.Close()
		return true, nil
	}
	if os.IsPermission(err) {
		// Device exists but not writable — we'll use sudo dd later.
		return false, nil
	}
	if os.IsNotExist(err) {
		return false, errs.NotFound("E_DEVICE_NOT_FOUND",
			fmt.Sprintf("device %s does not exist", device)).
			WithHint("list disks with: diskutil list (macOS) or lsblk (linux)")
	}
	return false, errs.FromGo(err)
}

// writePlanToDeviceSudo writes the flash plan's main image to a block device
// via sudo dd when the current user lacks direct write access. This allows
// nostos to run unprivileged (so 1Password CLI works) and only elevate for
// the raw device write.
//
// For .xz images, pipes xz decompression directly into sudo dd (no temp file).
func writePlanToDeviceSudo(ctx context.Context, plan osimage.FlashPlan, device string) (*image.Result, error) {
	main, ok := plan.MainImage()
	if !ok {
		return nil, errors.New("writePlanToDeviceSudo: FlashPlan has no main image part")
	}

	fi, err := os.Stat(main.Path)
	if err != nil {
		return nil, fmt.Errorf("stat image: %w", err)
	}

	fmt.Fprintf(os.Stderr, "→ elevating to write %s to %s (sudo dd)…\n", filepath.Base(main.Path), device)

	if filepath.Ext(main.Path) == ".xz" {
		// Pipe: xz -dc <image.xz> | sudo dd of=<device> bs=4m
		xzCmd := exec.CommandContext(ctx, "xz", "--decompress", "--stdout", main.Path)
		ddCmd := exec.CommandContext(ctx, "sudo", "dd", "of="+device, "bs=4m", "status=progress")

		pipe, err := xzCmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("create pipe: %w", err)
		}
		ddCmd.Stdin = pipe
		ddCmd.Stdout = os.Stdout
		ddCmd.Stderr = os.Stderr
		xzCmd.Stderr = os.Stderr

		if err := ddCmd.Start(); err != nil {
			return nil, fmt.Errorf("start sudo dd: %w", err)
		}
		if err := xzCmd.Run(); err != nil {
			_ = ddCmd.Process.Kill()
			return nil, fmt.Errorf("xz decompress: %w", err)
		}
		pipe.Close()
		if err := ddCmd.Wait(); err != nil {
			return nil, fmt.Errorf("sudo dd failed: %w", err)
		}
	} else {
		ddCmd := exec.CommandContext(ctx, "sudo", "dd",
			"if="+main.Path, "of="+device, "bs=4m", "status=progress")
		ddCmd.Stdout = os.Stdout
		ddCmd.Stderr = os.Stderr
		ddCmd.Stdin = os.Stdin
		if err := ddCmd.Run(); err != nil {
			return nil, fmt.Errorf("sudo dd failed: %w", err)
		}
	}

	// Eject the device after writing.
	if runtime.GOOS == "darwin" {
		fmt.Fprintf(os.Stderr, "→ ejecting %s…\n", device)
		ejectCmd := exec.CommandContext(ctx, "diskutil", "eject", device)
		ejectCmd.Stderr = os.Stderr
		_ = ejectCmd.Run() // best-effort; don't fail the flash on eject error
	}

	return &image.Result{
		ImagePath: device,
		BytesIn:   fi.Size(),
		BytesOut:  fi.Size(),
	}, nil
}
func shorten(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "…"
}

// humanBytes formats a byte count in MiB/GiB.
func humanBytes(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
	)
	switch {
	case n >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(GiB))
	case n >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(MiB))
	case n >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(KiB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ejectHint returns a platform-appropriate eject command for the device path.
func ejectHint(device string) string {
	switch runtime.GOOS {
	case "darwin":
		return "diskutil eject " + device
	case "linux":
		return "udisksctl power-off -b " + device
	default:
		return "sync && eject " + device
	}
}

// Compile-time check that flash is wired through the standard error helpers.
var _ = errors.New
var _ = strings.TrimSpace
