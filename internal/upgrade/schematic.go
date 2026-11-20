package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// DetectSchematic returns the factory schematic ID the node is running, read
// from the `schematic` ExtensionStatus that image-factory installers embed.
// Returns "" (no error) when the node exposes no schematic entry (vanilla
// image), so callers can treat it as "unknown" rather than drift.
func DetectSchematic(talosctlBin, talosconfigPath, nodeIP string) (string, error) {
	bin, err := resolveTalosctl(talosctlBin)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "get", "extensions",
		"--nodes", nodeIP, "--endpoints", nodeIP,
		"--talosconfig", talosconfigPath, "-o", "json").Output()
	if err != nil {
		return "", fmt.Errorf("talosctl get extensions (%s): %w", nodeIP, err)
	}
	return parseSchematic(out)
}

// parseSchematic scans the concatenated JSON objects `talosctl get -o json`
// prints and returns the version of the extension named "schematic".
func parseSchematic(out []byte) (string, error) {
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var r struct {
			Spec struct {
				Metadata struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"metadata"`
			} `json:"spec"`
		}
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			return "", fmt.Errorf("parse extensions: %w", err)
		}
		if r.Spec.Metadata.Name == "schematic" {
			return r.Spec.Metadata.Version, nil
		}
	}
}

// WaitSchematic polls until the node reports the wanted schematic. Needed for
// same-version reinstalls: the version gate in WaitHealthy passes immediately
// there, before the node has actually rebooted into the new image.
func WaitSchematic(talosctlBin, talosconfigPath, nodeIP, want string, timeout, interval time.Duration, progress func(string)) error {
	deadline := time.Now().Add(timeout)
	for {
		got, err := DetectSchematic(talosctlBin, talosconfigPath, nodeIP)
		if err == nil && got == want {
			if progress != nil {
				progress(fmt.Sprintf("✓ %s running schematic %s", nodeIP, short(want)))
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s to run schematic %s (last seen %q, err %v)", nodeIP, want, got, err)
		}
		if progress != nil {
			progress(fmt.Sprintf("  … %s schematic %s, waiting for %s", nodeIP, short(got), short(want)))
		}
		time.Sleep(interval)
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "<none>"
	}
	return s
}
