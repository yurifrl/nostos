// Package guestiso builds, publishes, and signs URLs for custom guest-VM
// install ISOs. Everything machine-specific comes from a config.Image entry;
// this package hardcodes no bucket, object, project, or machine name.
package guestiso

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/execx"
)

// buildScript is the container-side recipe, parametrized via env.
//
//go:embed build.sh
var buildScript []byte

// DefaultRuntime is the container runtime used for the privileged build.
const DefaultRuntime = "docker"

// DefaultImage is the build container base.
const DefaultImage = "debian:13"

// Builder assembles the combined ISO inside a privileged container.
type Builder struct {
	Cmd            execx.Commander // injected for testability
	Runtime        string          // defaults to DefaultRuntime
	BaseImage      string          // defaults to DefaultImage
	Stdout, Stderr io.Writer
}

// Build assembles the ISO for img and returns the local output path.
// configRoot is the directory the entry's relative answer_file resolves
// against; outDir is where the ISO is written (created if missing).
func (b *Builder) Build(ctx context.Context, img config.Image, configRoot, outDir string) (string, error) {
	runtime := b.Runtime
	if runtime == "" {
		runtime = DefaultRuntime
	}
	base := b.BaseImage
	if base == "" {
		base = DefaultImage
	}

	answer := filepath.Join(configRoot, img.Build.AnswerFile)
	if _, err := os.Stat(answer); err != nil {
		return "", fmt.Errorf("answer file %s: %w", answer, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir out dir: %w", err)
	}

	// Stage the embedded build script to a host file we can bind-mount.
	scriptDir, err := os.MkdirTemp("", "nostos-guestiso-")
	if err != nil {
		return "", err
	}
	scriptPath := filepath.Join(scriptDir, "build.sh")
	if err := os.WriteFile(scriptPath, buildScript, 0o755); err != nil {
		return "", err
	}

	outPath := filepath.Join(outDir, img.Store.Object)
	args := []string{
		"run", "--rm", "--privileged",
		"-v", answer + ":/ctx/autounattend.xml:ro",
		"-v", scriptPath + ":/build.sh:ro",
		"-v", outDir + ":/out",
		"-e", "UUP_ID=" + img.Build.UUPID,
		"-e", "EDITION=" + img.Build.Edition,
		"-e", "DRIVER_SOURCE=" + img.Build.DriverSource,
		"-e", "OUT_NAME=" + img.Store.Object,
		base, "bash", "/build.sh",
	}

	if err := b.Cmd.Run(ctx, runtime, args, nil, nil, b.Stdout, b.Stderr); err != nil {
		return "", fmt.Errorf("%s run (is the container runtime available?): %w", runtime, err)
	}
	return outPath, nil
}
