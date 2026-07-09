package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/yurifrl/nostos/internal/cli/errs"
	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/execx"
	"github.com/yurifrl/nostos/internal/guestiso"
	"github.com/yurifrl/nostos/internal/paths"
	"github.com/yurifrl/nostos/internal/secrets"
)

// newISOCmd wires `nostos iso` — build/publish/sign guest-VM install ISOs.
// Every verb takes a NAME and resolves all parameters from config.Images[NAME];
// nothing machine-specific is hardcoded here.
func newISOCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "iso",
		Short: "Build, publish, and sign guest-VM install ISOs (config-driven)",
		Long: "Manage custom guest-VM install media described by config `images` entries.\n" +
			"Each verb operates on a named entry: `nostos iso <verb> <name>`.\n\n" +
			"Prerequisites: a container runtime (docker) for `build`; the `op` CLI\n" +
			"for credential resolution on `publish`/`url`.",
	}
	cmd.AddCommand(newISOBuildCmd(), newISOPublishCmd(), newISOURLCmd(), newISOPrepareCmd())
	return cmd
}

func isoOutDir(p paths.Paths) string { return filepath.Join(p.State(), "iso") }

// resolveImage loads config and the named image entry.
func resolveImage(name string) (*config.Config, paths.Paths, config.Image, error) {
	cfg, p, err := loadConfig()
	if err != nil {
		return nil, paths.Paths{}, config.Image{}, err
	}
	img, err := cfg.ImageByName(name)
	if err != nil {
		return nil, paths.Paths{}, config.Image{}, errs.NotFound("E_IMAGE_NOT_FOUND", err.Error()).
			WithDetails(map[string]any{"name": name}).
			WithHint("add an `images:` entry to config.yaml")
	}
	return cfg, p, img, nil
}

// resolveImageSecrets resolves the entry's bucket ref + credentials_ref via the
// secrets backend (so no bucket name or key lives as a literal in config).
func resolveImageSecrets(cfg *config.Config, img config.Image) (bucket string, creds []byte, err error) {
	backends, err := secrets.BuildBackends(cfg)
	if err != nil {
		return "", nil, err
	}
	bucket, err = secrets.ResolveRefVia(backends, img.Store.Bucket.String())
	if err != nil {
		return "", nil, errs.Internal("E_ISO_BUCKET", err.Error()).
			WithHint("ensure store.bucket op:// ref resolves (op signin)")
	}
	credStr, err := secrets.ResolveRefVia(backends, img.CredentialsRef.String())
	if err != nil {
		return "", nil, errs.Internal("E_ISO_CREDS", err.Error()).
			WithHint("ensure the op:// credentials_ref resolves (op signin)")
	}
	return bucket, []byte(credStr), nil
}

func newISOBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build NAME",
		Short: "Build the combined install ISO for the named image entry",
		Args:  cobra.ExactArgs(1),
		RunE: runEFunc(func(cmd *cobra.Command, args []string) error {
			_, p, img, err := resolveImage(args[0])
			if err != nil {
				return err
			}
			out, err := isoBuild(cmd.Context(), p, img)
			if err != nil {
				return err
			}
			fmt.Fprintf(outWriter, "✓ built %s\n", out)
			return nil
		}),
	}
}

func newISOPublishCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "publish NAME",
		Short: "Upload the built ISO to the configured (private) object store",
		Args:  cobra.ExactArgs(1),
		RunE: runEFunc(func(cmd *cobra.Command, args []string) error {
			cfg, p, img, err := resolveImage(args[0])
			if err != nil {
				return err
			}
			uri, err := isoPublish(cmd.Context(), cfg, p, img)
			if err != nil {
				return err
			}
			fmt.Fprintf(outWriter, "✓ published %s\n", uri)
			return nil
		}),
	}
}

func newISOURLCmd() *cobra.Command {
	var dur time.Duration
	cmd := &cobra.Command{
		Use:   "url NAME",
		Short: "Mint a short-lived signed download URL + print a paste-ready snippet",
		Args:  cobra.ExactArgs(1),
		RunE: runEFunc(func(cmd *cobra.Command, args []string) error {
			cfg, _, img, err := resolveImage(args[0])
			if err != nil {
				return err
			}
			bucket, creds, err := resolveImageSecrets(cfg, img)
			if err != nil {
				return err
			}
			url, err := guestiso.SignURL(bucket, img.Store.Object, creds, dur)
			if err != nil {
				return errs.Internal("E_ISO_SIGN", err.Error())
			}
			fmt.Fprint(outWriter, guestiso.PasteSnippet(url))
			return nil
		}),
	}
	cmd.Flags().DurationVar(&dur, "duration", guestiso.MaxSignedURLTTL, "signed URL TTL (max 7 days)")
	return cmd
}

func newISOPrepareCmd() *cobra.Command {
	var dur time.Duration
	cmd := &cobra.Command{
		Use:   "prepare NAME",
		Short: "build → publish → url for the named image entry",
		Args:  cobra.ExactArgs(1),
		RunE: runEFunc(func(cmd *cobra.Command, args []string) error {
			cfg, p, img, err := resolveImage(args[0])
			if err != nil {
				return err
			}
			if _, err := isoBuild(cmd.Context(), p, img); err != nil {
				return err
			}
			bucket, creds, err := resolveImageSecrets(cfg, img)
			if err != nil {
				return err
			}
			isoPath := filepath.Join(isoOutDir(p), img.Store.Object)
			uri, err := guestiso.Publish(cmd.Context(), bucket, img.Store.Object, isoPath, creds)
			if err != nil {
				return errs.Internal("E_ISO_PUBLISH", err.Error())
			}
			fmt.Fprintf(outWriter, "✓ published %s\n", uri)
			url, err := guestiso.SignURL(bucket, img.Store.Object, creds, dur)
			if err != nil {
				return errs.Internal("E_ISO_SIGN", err.Error())
			}
			fmt.Fprint(outWriter, guestiso.PasteSnippet(url))
			return nil
		}),
	}
	cmd.Flags().DurationVar(&dur, "duration", guestiso.MaxSignedURLTTL, "signed URL TTL (max 7 days)")
	return cmd
}

func isoBuild(ctx context.Context, p paths.Paths, img config.Image) (string, error) {
	b := &guestiso.Builder{Cmd: execx.OSCommander{}, Stdout: outWriter, Stderr: outWriter}
	out, err := b.Build(ctx, img, p.Root(), isoOutDir(p))
	if err != nil {
		return "", errs.Internal("E_ISO_BUILD", err.Error()).
			WithHint("is a container runtime (docker) available?")
	}
	return out, nil
}

func isoPublish(ctx context.Context, cfg *config.Config, p paths.Paths, img config.Image) (string, error) {
	bucket, creds, err := resolveImageSecrets(cfg, img)
	if err != nil {
		return "", err
	}
	isoPath := filepath.Join(isoOutDir(p), img.Store.Object)
	uri, err := guestiso.Publish(ctx, bucket, img.Store.Object, isoPath, creds)
	if err != nil {
		return "", errs.Internal("E_ISO_PUBLISH", err.Error())
	}
	return uri, nil
}
