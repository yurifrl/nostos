// Package proxmox implements the OSImage for Proxmox VE. It consumes the
// lower-level resolver/download helpers in internal/netboot/proxmox and the
// memdisk (sanboot) iPXE template, exposing them behind the OSImage contract so
// flash / pxe serve never branch on "is this proxmox".
package proxmox

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/yurifrl/nostos/internal/config"
	nbproxmox "github.com/yurifrl/nostos/internal/netboot/proxmox"
	"github.com/yurifrl/nostos/internal/osimage"
	"github.com/yurifrl/nostos/internal/paths"
	"github.com/yurifrl/nostos/internal/pxe"
)

// Name is the registered OS name.
const Name = "proxmox"

func init() {
	osimage.Register(Name, func(d osimage.Deps) osimage.OSImage {
		return &Image{cfg: d.Cfg, paths: d.Paths}
	})
}

// Image is the Proxmox OSImage.
type Image struct {
	cfg   *config.Config
	paths paths.Paths
}

func (i *Image) Name() string { return Name }

func (i *Image) node(name string) (config.Node, error) {
	n, ok := i.cfg.Nodes[name]
	if !ok {
		return config.Node{}, fmt.Errorf("proxmox: node %q not found", name)
	}
	return n, nil
}

// version returns the node's Proxmox version selector ("latest" | pinned).
func (i *Image) version(n config.Node) (string, error) {
	if n.OS == nil || n.OS.Version == "" {
		return "", fmt.Errorf("proxmox: node has no version selector (set os.version)")
	}
	return n.OS.Version, nil
}

// Resolve maps the node's version selector to a concrete Ref (no download).
func (i *Image) Resolve(ctx context.Context, name string) (osimage.Ref, error) {
	n, err := i.node(name)
	if err != nil {
		return osimage.Ref{}, err
	}
	ver, err := i.version(n)
	if err != nil {
		return osimage.Ref{}, err
	}
	var resolver nbproxmox.Resolver
	spec, err := resolver.Resolve(ctx, ver)
	if err != nil {
		return osimage.Ref{}, err
	}
	return osimage.Ref{
		OS:      Name,
		Version: spec.Version,
		Arch:    n.Arch,
		ISOURL:  spec.ISOURL,
		SHA256:  spec.SHA256,
	}, nil
}

// NodeConfig: Proxmox installs no machineconfig.
func (i *Image) NodeConfig(ctx context.Context, name string, validate bool) ([]byte, error) {
	return nil, nil
}

// ensureISO downloads (and caches) the resolved ISO under the assets dir,
// returning the local path. Shared by NetbootScript and FlashPlan.
func (i *Image) ensureISO(ctx context.Context, ref osimage.Ref) (string, error) {
	spec := nbproxmox.BootSpec{Version: ref.Version, ISOURL: ref.ISOURL, SHA256: ref.SHA256}
	return nbproxmox.DownloadISO(ctx, spec, i.paths.Assets())
}

// NetbootScript ensures the ISO is cached (served over HTTP from /assets) and
// renders the sanboot stage-2 script.
func (i *Image) NetbootScript(ctx context.Context, name string, ref osimage.Ref) (string, error) {
	isoPath, err := i.ensureISO(ctx, ref)
	if err != nil {
		return "", err
	}
	return pxe.RenderProxmoxMemdisk(ref.Version, filepath.Base(isoPath)), nil
}

// FlashPlan downloads the ISO and returns it as the single main image part.
func (i *Image) FlashPlan(ctx context.Context, name string, ref osimage.Ref) (osimage.FlashPlan, error) {
	isoPath, err := i.ensureISO(ctx, ref)
	if err != nil {
		return osimage.FlashPlan{}, err
	}
	return osimage.FlashPlan{
		Parts: []osimage.FilePart{
			{Name: filepath.Base(isoPath), Path: isoPath, IsMainImage: true},
		},
		Notes: []string{
			fmt.Sprintf("Proxmox VE %s: boot the USB, run the installer, select the target disk", ref.Version),
		},
	}, nil
}
