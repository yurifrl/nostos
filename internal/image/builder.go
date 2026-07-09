// Package image is the OS-agnostic flash writer. It takes an osimage.FlashPlan
// (one main bootable image part plus zero or more sidecar parts) and writes it
// to a file or block device. It knows nothing about Talos, Proxmox,
// machineconfigs, or EEPROM — every OS-specific artifact is supplied as a part
// by the OSImage that produced the plan.
package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/yurifrl/nostos/internal/osimage"
)

// Mode determines where the main image goes.
type Mode int

const (
	// ModeFile writes the (optionally compressed) image to a file path.
	ModeFile Mode = iota
	// ModeDevice writes the image directly to a block device. The caller is
	// responsible for confirming the device is writable / unmounted.
	ModeDevice
)

// Dest describes the write destination.
type Dest struct {
	Mode     Mode
	Path     string // file path (ModeFile) or device path (ModeDevice)
	Compress bool   // xz-compress (ModeFile only)
}

// Result describes what Write produced.
type Result struct {
	ImagePath string   `json:"image_path"`
	Sidecars  []string `json:"sidecars,omitempty"`
	BytesIn   int64    `json:"bytes_in"`
	BytesOut  int64    `json:"bytes_out"`
}

// Write streams the plan's main image to dest and, in file mode, writes each
// sidecar part beside the output. The main image is decompressed when its
// source path ends in .xz.
func Write(ctx context.Context, plan osimage.FlashPlan, dest Dest) (*Result, error) {
	main, ok := plan.MainImage()
	if !ok {
		return nil, errors.New("image.Write: FlashPlan has no main image part")
	}
	if dest.Path == "" {
		return nil, errors.New("image.Write: empty destination path")
	}
	if dest.Mode == ModeDevice && dest.Compress {
		return nil, errors.New("image.Write: --compress is incompatible with a device destination")
	}
	if _, err := os.Stat(main.Path); err != nil {
		return nil, fmt.Errorf("image.Write: main image not found: %w", err)
	}

	res := &Result{}
	out, outPath, cleanup, err := openOutput(dest)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	res.ImagePath = outPath

	src, err := os.Open(main.Path)
	if err != nil {
		return nil, fmt.Errorf("open main image: %w", err)
	}
	defer src.Close()
	if fi, _ := src.Stat(); fi != nil {
		res.BytesIn = fi.Size()
	}

	var reader io.Reader = src
	total := res.BytesIn // bytes to write (== source size for a raw image)
	if filepath.Ext(main.Path) == ".xz" {
		xr, err := xz.NewReader(src)
		if err != nil {
			return nil, fmt.Errorf("xz reader: %w", err)
		}
		reader = xr
		total = -1 // decompressed size unknown ahead of time
	}

	pw := &writeProgress{total: total, label: "writing " + filepath.Base(outPath), start: time.Now(), last: time.Now()}
	n, err := io.Copy(io.MultiWriter(out, pw), reader)
	if err != nil {
		return nil, fmt.Errorf("write image: %w", err)
	}
	pw.finish()
	res.BytesOut = n

	// Sidecars are only meaningful for a file destination (a device has no
	// adjacent filesystem to drop them on).
	if dest.Mode == ModeFile {
		for _, part := range plan.Sidecars() {
			dst := sidecarPath(outPath, part.Name)
			if err := copyFileImage(part.Path, dst); err != nil {
				return nil, fmt.Errorf("write sidecar %s: %w", part.Name, err)
			}
			res.Sidecars = append(res.Sidecars, dst)
		}
	}
	return res, nil
}

// openOutput returns the main-image writer, the final path, and a cleanup fn.
func openOutput(dest Dest) (io.Writer, string, func(), error) {
	switch dest.Mode {
	case ModeFile:
		path := dest.Path
		if dest.Compress && filepath.Ext(path) != ".xz" {
			path += ".xz"
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, "", nil, fmt.Errorf("create image output: %w", err)
		}
		if dest.Compress {
			enc, err := xz.NewWriter(f)
			if err != nil {
				f.Close()
				return nil, "", nil, fmt.Errorf("xz writer: %w", err)
			}
			return enc, path, func() { enc.Close(); f.Close() }, nil
		}
		return f, path, func() { f.Close() }, nil
	case ModeDevice:
		f, err := os.OpenFile(dest.Path, os.O_WRONLY|os.O_SYNC, 0o600)
		if err != nil {
			return nil, "", nil, fmt.Errorf("open device %s: %w", dest.Path, err)
		}
		return f, dest.Path, func() { f.Close() }, nil
	default:
		return nil, "", nil, fmt.Errorf("image.Write: unknown mode %d", dest.Mode)
	}
}

// sidecarPath drops trailing .xz / .raw / .img and appends the suffix.
func sidecarPath(out, suffix string) string {
	base := out
	for {
		ext := filepath.Ext(base)
		if ext == ".xz" || ext == ".raw" || ext == ".img" {
			base = base[:len(base)-len(ext)]
			continue
		}
		break
	}
	return base + suffix
}

// writeProgress prints a throttled, carriage-return-updating line to stderr as
// the image is written to a device/file, so a multi-minute write to a slow USB
// stick is visibly alive rather than looking hung. total is -1 when unknown
// (e.g. an xz-decompressed stream).
type writeProgress struct {
	total   int64
	label   string
	done    int64
	start   time.Time
	last    time.Time
	lastLen int
}

func (p *writeProgress) Write(b []byte) (int, error) {
	n := len(b)
	p.done += int64(n)
	if time.Since(p.last) >= time.Second {
		p.print("\r")
		p.last = time.Now()
	}
	return n, nil
}

func (p *writeProgress) print(prefix string) {
	mib := func(x int64) float64 { return float64(x) / (1024 * 1024) }
	elapsed := time.Since(p.start).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = mib(p.done) / elapsed
	}
	var line string
	if p.total > 0 {
		pct := float64(p.done) / float64(p.total) * 100
		line = fmt.Sprintf("→ %s: %.0f/%.0f MiB (%.0f%%) at %.1f MiB/s",
			p.label, mib(p.done), mib(p.total), pct, rate)
	} else {
		line = fmt.Sprintf("→ %s: %.0f MiB at %.1f MiB/s", p.label, mib(p.done), rate)
	}
	if pad := p.lastLen - len(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	p.lastLen = len(line)
	fmt.Fprint(os.Stderr, prefix+line)
}

func (p *writeProgress) finish() {
	p.print("\r")
	fmt.Fprintln(os.Stderr)
}
