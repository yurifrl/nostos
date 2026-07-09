// Package osimage abstracts WHAT operating system nostos installs onto a node,
// as a peer to internal/provisioner (which abstracts HOW a node boots).
//
// The two axes compose: a provisioner (pxe, tpi, flash) drives a transport and
// lifecycle, while an OSImage (talos, proxmox) owns packaging — resolving a
// version to a concrete image, rendering any node config, rendering the
// per-MAC netboot script, and producing a flash plan. Each OSImage registers
// itself in init(), exactly like a provisioner Factory, so a new OS is a new
// file with zero edits to flash / pxe serve / render.
package osimage

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/paths"
)

// Ref is a concrete, resolved image identity (post "latest"/schematic
// resolution). Each OSImage populates the fields it needs.
type Ref struct {
	OS      string // "talos" | "proxmox"
	Version string // concrete, e.g. "v1.13.3" or "8.10-1"
	Arch    string // "amd64" | "arm64"
	ISOURL  string // proxmox: full ISO URL (empty for talos)
	SHA256  string // when known (auditability for "latest")
}

// FilePart is one artifact written during a flash: the main bootable image or
// a sidecar (e.g. a Talos machineconfig, an RPi EEPROM image).
type FilePart struct {
	Name        string // suggested filename / suffix
	Path        string // local source path
	IsMainImage bool   // exactly one part is the bootable image
}

// FlashPlan is what an OSImage hands the OS-agnostic image writer.
type FlashPlan struct {
	Parts []FilePart // exactly one IsMainImage; rest are sidecars
	Notes []string   // operator next-steps (e.g. "apply-config …")
}

// MainImage returns the single main-image part, or false when absent.
func (fp FlashPlan) MainImage() (FilePart, bool) {
	for _, p := range fp.Parts {
		if p.IsMainImage {
			return p, true
		}
	}
	return FilePart{}, false
}

// Sidecars returns the non-main parts.
func (fp FlashPlan) Sidecars() []FilePart {
	var out []FilePart
	for _, p := range fp.Parts {
		if !p.IsMainImage {
			out = append(out, p)
		}
	}
	return out
}

// Deps is the set of seams an OSImage needs, mirroring provisioner.Deps.
type Deps struct {
	Cfg   *config.Config
	Paths paths.Paths
}

// OSImage is the packaging contract every installable OS implements. It owns
// NO install lifecycle (preflight/boot/apply) — that stays in provisioner.
type OSImage interface {
	Name() string

	// Resolve turns a node's version selector into a concrete Ref. It performs
	// only the lookup/resolution; downloading is internal to the methods that
	// need a local artifact (NetbootScript, FlashPlan).
	Resolve(ctx context.Context, nodeName string) (Ref, error)

	// NodeConfig renders any config this OS applies after boot. Talos: the
	// machineconfig bytes (validate toggles talosctl validate). Proxmox: returns
	// (nil, nil) — no machineconfig.
	NodeConfig(ctx context.Context, nodeName string, validate bool) ([]byte, error)

	// NetbootScript renders the per-MAC stage-2 iPXE script for this OS.
	NetbootScript(ctx context.Context, nodeName string, ref Ref) (string, error)

	// FlashPlan returns the parts to write to a USB/device for this OS.
	FlashPlan(ctx context.Context, nodeName string, ref Ref) (FlashPlan, error)
}

// Factory builds an OSImage given seams. Mirrors provisioner.Factory.
type Factory func(Deps) OSImage

var (
	regMu    sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a Factory under an OS name. Panics on empty/nil/duplicate,
// matching provisioner.Register's fail-fast contract.
func Register(name string, f Factory) {
	if name == "" {
		panic("osimage.Register: empty name")
	}
	if f == nil {
		panic(fmt.Sprintf("osimage.Register: nil factory for %q", name))
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, ok := registry[name]; ok {
		panic(fmt.Sprintf("osimage.Register: duplicate name %q", name))
	}
	registry[name] = f
}

// ErrNotRegistered is returned by For/New when no OSImage is registered for the
// requested OS name.
var ErrNotRegistered = fmt.Errorf("osimage: not registered")

// New constructs the OSImage registered under name.
func New(name string, deps Deps) (OSImage, error) {
	regMu.RLock()
	f, ok := registry[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("osimage %q: %w", name, ErrNotRegistered)
	}
	return f(deps), nil
}

// For selects the OSImage for a node, defaulting to "talos" when the node
// declares no OS. The OS name is sourced from the node's effective target.
func For(deps Deps, nodeName string) (OSImage, error) {
	node, ok := deps.Cfg.Nodes[nodeName]
	if !ok {
		return nil, fmt.Errorf("osimage.For: node %q not found", nodeName)
	}
	return New(nodeOS(node), deps)
}

// nodeOS returns a node's OS name, defaulting to "talos". Today it reads the
// (transitional) boot.pxe.target; the P4 config migration moves this to a
// node-level os.name without changing this function's contract.
func nodeOS(node config.Node) string {
	return node.OSName()
}

// Names returns the sorted registered OS names (for diagnostics/help).
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
