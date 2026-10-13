package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPlanK8sHops(t *testing.T) {
	must := func(s string) Version {
		v, err := ParseVersion(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return v
	}

	tests := []struct {
		name    string
		floor   Version
		target  Version
		want    []string
		wantErr bool
	}{
		{name: "single minor", floor: must("v1.32.1"), target: must("v1.33.1"), want: []string{"v1.33.1"}},
		{name: "two minors", floor: must("v1.31.0"), target: must("v1.33.0"), want: []string{"v1.32.0", "v1.33.0"}},
		{name: "three minors", floor: must("v1.30.2"), target: must("v1.33.1"), want: []string{"v1.31.0", "v1.32.0", "v1.33.1"}},
		{name: "patch only", floor: must("v1.33.0"), target: must("v1.33.1"), want: []string{"v1.33.1"}},
		{name: "already at target", floor: must("v1.33.1"), target: must("v1.33.1"), want: nil},
		{name: "downgrade refused", floor: must("v1.34.0"), target: must("v1.33.1"), wantErr: true},
		{name: "cross-major refused", floor: must("v1.33.1"), target: must("v2.1.0"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanK8sHops(tc.floor, tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got hops %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanK8sHops: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d hops %v, want %d: %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i].String() != tc.want[i] {
					t.Fatalf("hop %d = %s, want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSyncKubeletTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.yaml")
	body := "machine:\n  kubelet:\n    image: ghcr.io/siderolabs/kubelet:v1.32.1\n    extraArgs: {}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	target, _ := ParseVersion("v1.37.1")

	found, changed, err := SyncKubeletTemplate(path, target)
	if err != nil || !found || !changed {
		t.Fatalf("first sync: found=%v changed=%v err=%v", found, changed, err)
	}
	out, _ := os.ReadFile(path)
	if want := "ghcr.io/siderolabs/kubelet:v1.37.1"; !contains(string(out), want) {
		t.Fatalf("template missing %q:\n%s", want, out)
	}
	if contains(string(out), "v1.32.1") {
		t.Fatalf("old tag still present:\n%s", out)
	}

	found, changed, err = SyncKubeletTemplate(path, target)
	if err != nil || !found || changed {
		t.Fatalf("second sync (idempotent): found=%v changed=%v err=%v", found, changed, err)
	}
}

func TestSyncKubeletTemplateNoPin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(path, []byte("machine:\n  network: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, _ := ParseVersion("v1.37.1")
	found, changed, err := SyncKubeletTemplate(path, target)
	if err != nil || found || changed {
		t.Fatalf("no-pin template: found=%v changed=%v err=%v", found, changed, err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestKubeImageRE(t *testing.T) {
	cases := []struct {
		image string
		comp  string
		tag   string
	}{
		{"registry.k8s.io/kube-apiserver:v1.37.1", "apiserver", "1.37.1"},
		{"registry.k8s.io/kube-controller-manager:v1.37.1", "controller-manager", "1.37.1"},
		{"registry.k8s.io/kube-scheduler:v1.37.1", "scheduler", "1.37.1"},
		{"registry.k8s.io/kube-proxy:v1.37.1", "proxy", "1.37.1"},
		{"ghcr.io/siderolabs/kube-apiserver:v1.33.0", "apiserver", "1.33.0"},
		{"quay.io/cilium/cilium:v1.20.0@sha256:abc", "", ""}, // untracked, no match
		{"registry.k8s.io/coredns/coredns:v1.14.7", "", ""},  // untracked, no match
	}
	for _, tc := range cases {
		m := kubeImageRE.FindStringSubmatch(tc.image)
		if tc.comp == "" {
			if m != nil {
				t.Errorf("kubeImageRE(%q) = %v, want no match", tc.image, m)
			}
			continue
		}
		if m == nil || m[1] != tc.comp || m[2] != tc.tag {
			t.Errorf("kubeImageRE(%q) = %v, want [%s %s]", tc.image, m, tc.comp, tc.tag)
		}
	}
}
