// Package proxmox resolves a Proxmox VE release selector ("latest" or a pinned
// version like "8.3-1") to concrete netboot artifacts, using built-in knowledge
// of the Proxmox download layout. The operator never supplies a URL.
package proxmox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultIndexURL is the Proxmox ISO directory index nostos scrapes to discover
// published releases. Overridable (var, not const) so tests can point at a
// local httptest server and never touch the live network.
var DefaultIndexURL = "https://enterprise.proxmox.com/iso/"

// ErrVersionNotFound is returned when a pinned version is absent from the index.
var ErrVersionNotFound = errors.New("proxmox: version not found in index")

// ErrNoReleases is returned when the index contains no parseable releases.
var ErrNoReleases = errors.New("proxmox: no releases found in index")

// BootSpec is the resolved set of netboot artifacts for one Proxmox release.
//
// The memdisk (full-ISO-to-RAM) boot path uses ISOURL. KernelURL/InitrdURL/
// Cmdline are reserved for the later kernel+initrd-direct optimization and are
// empty in the memdisk path.
type BootSpec struct {
	Version   string // concrete resolved version, e.g. "8.3-1"
	ISOURL    string // full URL to the .iso
	SHA256    string // published checksum of the ISO, when discoverable
	KernelURL string // reserved (kernel+initrd-direct path)
	InitrdURL string // reserved (kernel+initrd-direct path)
	Cmdline   string // reserved (kernel+initrd-direct path)
}

// Version is a parsed Proxmox release tuple (major.minor-build).
type Version struct {
	Major, Minor, Build int
	File                string // the ISO filename, e.g. "proxmox-ve_8.3-1.iso"
}

// String renders the version selector form, e.g. "8.3-1".
func (v Version) String() string { return fmt.Sprintf("%d.%d-%d", v.Major, v.Minor, v.Build) }

// Less orders by numeric tuple (NOT lexical): 8.10-1 > 8.9-1.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Build < o.Build
}

// isoFileRE matches "proxmox-ve_<major>.<minor>-<build>.iso".
var isoFileRE = regexp.MustCompile(`proxmox-ve_(\d+)\.(\d+)-(\d+)\.iso`)

// ParseIndex extracts every proxmox-ve_X.Y-Z.iso release from an index page
// body. Pure (no I/O), deduplicated, so it is trivially unit-testable against a
// captured fixture.
func ParseIndex(body string) []Version {
	seen := map[string]bool{}
	var out []Version
	for _, m := range isoFileRE.FindAllStringSubmatch(body, -1) {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		build, _ := strconv.Atoi(m[3])
		file := m[0]
		if seen[file] {
			continue
		}
		seen[file] = true
		out = append(out, Version{Major: major, Minor: minor, Build: build, File: file})
	}
	return out
}

// SelectLatest returns the highest version by numeric tuple. Returns
// ErrNoReleases when the slice is empty.
func SelectLatest(versions []Version) (Version, error) {
	if len(versions) == 0 {
		return Version{}, ErrNoReleases
	}
	sorted := append([]Version(nil), versions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Less(sorted[j]) })
	return sorted[len(sorted)-1], nil
}

// FindPinned returns the version whose selector equals want (e.g. "8.3-1").
// Returns ErrVersionNotFound when absent.
func FindPinned(versions []Version, want string) (Version, error) {
	for _, v := range versions {
		if v.String() == want {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("%w: %s", ErrVersionNotFound, want)
}

// Resolver turns a version selector into a BootSpec by reading the Proxmox
// index. The zero value is usable (DefaultIndexURL + a 30s HTTP client).
type Resolver struct {
	IndexURL string
	Client   *http.Client
}

func (r *Resolver) indexURL() string {
	if r.IndexURL != "" {
		return r.IndexURL
	}
	return DefaultIndexURL
}

func (r *Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Resolve maps version ("latest" | pinned like "8.3-1") to a BootSpec. It
// fetches the index once, parses it, selects the release, and best-effort
// attaches the published checksum. Network errors and not-found are returned as
// distinct errors so callers can map exit codes.
func (r *Resolver) Resolve(ctx context.Context, version string) (BootSpec, error) {
	body, err := r.fetch(ctx, r.indexURL())
	if err != nil {
		return BootSpec{}, fmt.Errorf("proxmox: fetch index: %w", err)
	}
	versions := ParseIndex(body)

	var chosen Version
	if version == "latest" {
		chosen, err = SelectLatest(versions)
	} else {
		chosen, err = FindPinned(versions, version)
	}
	if err != nil {
		return BootSpec{}, err
	}

	base := r.indexURL()
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	spec := BootSpec{
		Version: chosen.String(),
		ISOURL:  base + chosen.File,
	}
	// Best-effort checksum: parse a SHA256SUMS file if the index publishes one.
	if sum := r.lookupChecksum(ctx, base, chosen.File); sum != "" {
		spec.SHA256 = sum
	}
	return spec, nil
}

// lookupChecksum tries SHA256SUMS then SHA256SUMS.txt; returns the hash for
// file, or "" on any miss. Never fatal — checksum is auditability, not gating.
func (r *Resolver) lookupChecksum(ctx context.Context, base, file string) string {
	for _, name := range []string{"SHA256SUMS", "SHA256SUMS.txt"} {
		body, err := r.fetch(ctx, base+name)
		if err != nil {
			continue
		}
		if sum := ParseChecksums(body, file); sum != "" {
			return sum
		}
	}
	return ""
}

// ParseChecksums finds the sha256 hex for file in a "SHA256SUMS"-style body
// (lines of "<hex>  <filename>"). Pure, unit-testable.
func ParseChecksums(body, file string) string {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == file {
			return fields[0]
		}
	}
	return ""
}

func (r *Resolver) fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap on an index page
	if err != nil {
		return "", err
	}
	return string(b), nil
}
