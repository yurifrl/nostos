// Package talos implements the OSImage for Talos Linux by wrapping nostos's
// existing Talos code paths (factory image download, machineconfig render, and
// the boot.ipxe template). It introduces no new Talos behavior — it is the
// extraction of the previously-implicit "Talos is what nostos installs"
// assumption into an explicit, registered OSImage.
package talos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/image"
	"github.com/yurifrl/nostos/internal/osimage"
	"github.com/yurifrl/nostos/internal/paths"
	"github.com/yurifrl/nostos/internal/pxe"
	"github.com/yurifrl/nostos/internal/registry"
)

// Name is the registered OS name.
const Name = "talos"

func init() {
	osimage.Register(Name, func(d osimage.Deps) osimage.OSImage {
		return &Image{cfg: d.Cfg, paths: d.Paths}
	})
}

// Image is the Talos OSImage.
type Image struct {
	cfg   *config.Config
	paths paths.Paths
}

func (i *Image) Name() string { return Name }

func (i *Image) node(name string) (config.Node, error) {
	n, ok := i.cfg.Nodes[name]
	if !ok {
		return config.Node{}, fmt.Errorf("talos: node %q not found", name)
	}
	return n, nil
}

// Resolve returns the Talos image identity: the cluster Talos version + the
// node's arch (schematic is applied by the asset download path).
func (i *Image) Resolve(ctx context.Context, name string) (osimage.Ref, error) {
	n, err := i.node(name)
	if err != nil {
		return osimage.Ref{}, err
	}
	return osimage.Ref{OS: Name, Version: i.cfg.Cluster.TalosVersion, Arch: n.Arch}, nil
}

// NodeConfig renders the Talos machineconfig via the existing registry.Render
// (which also writes it under state/configs and validates), then returns the
// bytes. The file side effect is preserved for callers that consume the path.
func (i *Image) NodeConfig(ctx context.Context, name string, validate bool) ([]byte, error) {
	path, err := registry.Render(i.cfg, i.paths, name, validate)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// NetbootScript renders the Talos stage-2 iPXE script (kernel + initramfs +
// talos.config), identical to the build-time boot.ipxe body.
func (i *Image) NetbootScript(ctx context.Context, name string, ref osimage.Ref) (string, error) {
	n, err := i.node(name)
	if err != nil {
		return "", err
	}
	arch := n.Arch
	if arch == "" {
		arch = "amd64"
	}
	// Same body RenderBootIpxe writes; MAC/next-server are runtime iPXE vars.
	return fmt.Sprintf(pxe.BootIpxeTemplate, i.cfg.Cluster.TalosVersion, arch, "", arch), nil
}

// FlashPlan downloads the Talos raw image and assembles the parts: the raw
// image (main), the machineconfig sidecar, and (for rpi_generic) the EEPROM
// firmware dir. Mirrors what the existing flash path produces.
func (i *Image) FlashPlan(ctx context.Context, name string, ref osimage.Ref) (osimage.FlashPlan, error) {
	n, err := i.node(name)
	if err != nil {
		return osimage.FlashPlan{}, err
	}
	spec := pxe.AssetSpec{
		Schematic: n.EffectiveSchematic(i.cfg.Cluster),
		Arch:      n.Arch,
		Version:   i.cfg.Cluster.TalosVersion,
		IsRPi:     n.Overlay == "rpi_generic",
	}
	rawPath, err := pxe.DownloadTalosRawImage(ctx, i.paths, spec)
	if err != nil {
		return osimage.FlashPlan{}, err
	}
	plan := osimage.FlashPlan{
		Parts: []osimage.FilePart{
			{Name: filepath.Base(rawPath), Path: rawPath, IsMainImage: true},
		},
	}
	// Machineconfig sidecar: NodeConfig renders + writes it to ConfigPath.
	if _, err := i.NodeConfig(ctx, name, true); err != nil {
		return osimage.FlashPlan{}, err
	}
	cfgPath := registry.ConfigPath(i.paths, n, name)
	plan.Parts = append(plan.Parts, osimage.FilePart{Name: "-config.yaml", Path: cfgPath})
	plan.Notes = append(plan.Notes,
		fmt.Sprintf("apply config once the node is up: nostos apply %s", name))

	// RPi EEPROM recovery dir: assembled to a standalone directory the operator
	// copies to a FAT32 SD (separate from the image), surfaced via Notes rather
	// than as an image sidecar.
	if spec.IsRPi {
		if err := pxe.DownloadRPiFirmware(ctx, i.paths); err != nil {
			return osimage.FlashPlan{}, err
		}
		fwDir := filepath.Join(i.paths.Assets(), "rpi-firmware")
		eepromDir := filepath.Join(i.paths.Assets(), n.MACHyphen()+"-eeprom")
		if err := image.WriteEEPROMImage(eepromDir, fwDir); err != nil {
			return osimage.FlashPlan{}, err
		}
		plan.Notes = append(plan.Notes,
			fmt.Sprintf("RPi 4 first boot: copy %s/* to a FAT32 SD, boot once to flash EEPROM, then swap to the main disk", eepromDir))
	}
	return plan, nil
}
