package proxmox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// DownloadISO fetches spec.ISOURL into destDir, caching by filename: if the
// file already exists (and, when a checksum is known, matches), the download is
// skipped. Returns the local path to the cached ISO.
//
// This is what makes `latest` resolve/download once per serve rather than per
// PXE request: the serve loop calls DownloadISO at startup and then serves the
// cached file over HTTP.
func DownloadISO(ctx context.Context, spec BootSpec, destDir string) (string, error) {
	if spec.ISOURL == "" {
		return "", fmt.Errorf("proxmox: empty ISO URL in boot spec")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	name := path.Base(spec.ISOURL)
	dst := filepath.Join(destDir, name)

	// Cache hit: file exists and (if we have a checksum) verifies.
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		if spec.SHA256 == "" || verifySHA256(dst, spec.SHA256) {
			return dst, nil
		}
		// Checksum mismatch → re-download.
		_ = os.Remove(dst)
	}

	if err := downloadTo(ctx, spec.ISOURL, dst); err != nil {
		return "", err
	}
	if spec.SHA256 != "" && !verifySHA256(dst, spec.SHA256) {
		_ = os.Remove(dst)
		return "", fmt.Errorf("proxmox: checksum mismatch for %s", name)
	}
	return dst, nil
}

func downloadTo(ctx context.Context, url, dst string) error {
	tmp := dst + ".part"

	// Resume support: if a partial .part exists, ask the server to continue from
	// where we left off (Range). Falls back to a fresh download when the server
	// ignores Range (200 instead of 206).
	var resumeFrom int64
	if fi, err := os.Stat(tmp); err == nil && fi.Size() > 0 {
		resumeFrom = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}
	client := &http.Client{Timeout: 60 * time.Minute} // slow mirrors + ~1.5 GB
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Decide append-vs-truncate from the server's answer.
	appendMode := false
	switch resp.StatusCode {
	case http.StatusPartialContent: // 206 — resume accepted
		appendMode = true
	case http.StatusOK: // 200 — server ignored Range; start over
		resumeFrom = 0
	default:
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(tmp, flags, 0o644)
	if err != nil {
		return err
	}

	// Total = bytes already on disk + bytes this response will deliver.
	total := resp.ContentLength
	if total > 0 {
		total += resumeFrom
	}
	pw := &progressWriter{total: total, done: resumeFrom, label: path.Base(dst), start: time.Now(), last: time.Now()}
	if resumeFrom > 0 {
		fmt.Fprintf(os.Stderr, "→ resuming %s from %.0f MiB\n", pw.label, float64(resumeFrom)/(1024*1024))
	}
	if _, err := io.Copy(io.MultiWriter(f, pw), resp.Body); err != nil {
		f.Close()
		return err // keep .part for the next resume
	}
	pw.finish()
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// progressWriter prints a throttled, carriage-return-updating download progress
// line to stderr (≈ once/sec), so a multi-minute ISO fetch is visibly alive
// rather than looking hung.
type progressWriter struct {
	total   int64 // -1 when Content-Length is unknown
	label   string
	done    int64
	start   time.Time
	last    time.Time
	lastLen int
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.done += int64(n)
	if time.Since(p.last) >= time.Second {
		p.print("\r")
		p.last = time.Now()
	}
	return n, nil
}

func (p *progressWriter) print(prefix string) {
	mib := func(x int64) float64 { return float64(x) / (1024 * 1024) }
	elapsed := time.Since(p.start).Seconds()
	rate := 0.0
	if elapsed > 0 {
		rate = mib(p.done) / elapsed
	}
	var line string
	if p.total > 0 {
		pct := float64(p.done) / float64(p.total) * 100
		line = fmt.Sprintf("\u2192 downloading %s: %.0f/%.0f MiB (%.0f%%) at %.1f MiB/s",
			p.label, mib(p.done), mib(p.total), pct, rate)
	} else {
		line = fmt.Sprintf("\u2192 downloading %s: %.0f MiB at %.1f MiB/s",
			p.label, mib(p.done), rate)
	}
	// Pad to clear any leftover chars from a longer previous line.
	if pad := p.lastLen - len(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	p.lastLen = len(line)
	fmt.Fprint(os.Stderr, prefix+line)
}

func (p *progressWriter) finish() {
	p.print("\r")
	fmt.Fprintln(os.Stderr)
}

func verifySHA256(path, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == want
}
