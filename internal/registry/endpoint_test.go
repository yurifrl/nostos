package registry

import (
	"strings"
	"testing"

	"github.com/yurifrl/nostos/internal/config"
	"gopkg.in/yaml.v3"
)

func endpointTestConfig() *config.Config {
	return &config.Config{
		Cluster: config.Cluster{Endpoint: "https://api.k8s.lan:6443"},
		Nodes: map[string]config.Node{
			"dell01":     {IP: "192.168.68.100", TailscaleIP: "100.82.148.37", Role: "controlplane"},
			"rpi01":      {IP: "192.168.0.170", TailscaleIP: "100.83.143.13", Role: "controlplane"},
			"macintel01": {IP: "192.168.68.91", TailscaleIP: "100.98.116.94", Role: "controlplane"},
			"tp1":        {IP: "192.168.68.107", Role: "worker"},
		},
	}
}

func parseFirstDoc(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(splitYAMLDocuments(body)[0]), &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func TestInjectControlPlaneEndpoint_ControlPlane(t *testing.T) {
	cfg := endpointTestConfig()
	tmpl := "machine:\n  type: controlplane\ncluster:\n  controlPlane:\n    endpoint: https://192.168.68.100:6443\n  apiServer:\n    certSANs:\n      - 192.168.68.100\n"

	out, err := injectControlPlaneEndpoint(tmpl, cfg, cfg.Nodes["macintel01"])
	if err != nil {
		t.Fatal(err)
	}
	m := parseFirstDoc(t, out)

	cp := m["cluster"].(map[string]any)["controlPlane"].(map[string]any)
	if cp["endpoint"] != "https://api.k8s.lan:6443" {
		t.Errorf("endpoint not overwritten: %v", cp["endpoint"])
	}

	// extraHostEntries: every CP address, all aliased to the endpoint host.
	entries := m["machine"].(map[string]any)["network"].(map[string]any)["extraHostEntries"].([]any)
	if len(entries) != 6 { // 3 CPs x (LAN + TS)
		t.Fatalf("want 6 host entries, got %d", len(entries))
	}
	for _, want := range []string{"192.168.68.100", "100.82.148.37", "100.83.143.13", "192.168.68.91"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing CP address %s in output", want)
		}
	}
	first := entries[0].(map[string]any)
	if first["aliases"].([]any)[0] != "api.k8s.lan" {
		t.Errorf("entry not aliased to endpoint host: %v", first["aliases"])
	}

	// certSANs: hand-written SAN preserved, endpoint host appended once.
	sans := m["cluster"].(map[string]any)["apiServer"].(map[string]any)["certSANs"].([]any)
	if len(sans) != 2 || sans[0] != "192.168.68.100" || sans[1] != "api.k8s.lan" {
		t.Errorf("certSANs wrong: %v", sans)
	}
}

func TestInjectControlPlaneEndpoint_WorkerNoCertSANs(t *testing.T) {
	cfg := endpointTestConfig()
	// Worker template with no machine.network and no cluster.apiServer.
	tmpl := "machine:\n  type: worker\ncluster:\n  controlPlane:\n    endpoint: https://192.168.68.100:6443\n"

	out, err := injectControlPlaneEndpoint(tmpl, cfg, cfg.Nodes["tp1"])
	if err != nil {
		t.Fatal(err)
	}
	m := parseFirstDoc(t, out)

	cp := m["cluster"].(map[string]any)["controlPlane"].(map[string]any)
	if cp["endpoint"] != "https://api.k8s.lan:6443" {
		t.Errorf("worker endpoint not set: %v", cp["endpoint"])
	}
	if _, ok := m["machine"].(map[string]any)["network"].(map[string]any)["extraHostEntries"]; !ok {
		t.Error("worker missing extraHostEntries")
	}
	if _, ok := m["cluster"].(map[string]any)["apiServer"]; ok {
		t.Error("worker must not get apiServer.certSANs")
	}
}
