package registry

import (
	"fmt"
	"strings"

	"github.com/yurifrl/nostos/internal/config"
	"gopkg.in/yaml.v3"
)

// injectControlPlaneEndpoint derives the HA control-plane endpoint from config
// and splices it into the rendered machineconfig, so no template hand-writes a
// single control-plane IP (one dead CP would otherwise down the whole API path).
//
// On every node it sets:
//   - cluster.controlPlane.endpoint = cfg.Cluster.Endpoint (the stable name)
//   - machine.network.extraHostEntries = one entry per CP address, all aliased
//     to the endpoint hostname, so the name resolves locally (no DNS at boot).
//
// On controlplane nodes it additionally appends the hostname to
// cluster.apiServer.certSANs so the apiserver serving cert is valid for the name.
func injectControlPlaneEndpoint(body string, cfg *config.Config, node config.Node) (string, error) {
	host, err := cfg.Cluster.EndpointHost()
	if err != nil {
		return "", err
	}
	addrs := cfg.ControlPlaneEndpointAddrs()
	if len(addrs) == 0 {
		return "", fmt.Errorf("no controlplane nodes in config to build endpoint %q", cfg.Cluster.Endpoint)
	}

	docs := splitYAMLDocuments(body)
	if len(docs) == 0 {
		return "", fmt.Errorf("empty template body")
	}
	var first map[string]any
	if err := yaml.Unmarshal([]byte(docs[0]), &first); err != nil {
		return "", fmt.Errorf("parse first template document: %w", err)
	}

	// machine.network.extraHostEntries: <host> -> every CP address.
	machine := childMap(first, "machine")
	network := childMap(machine, "network")
	entries := make([]any, 0, len(addrs))
	for _, ip := range addrs {
		entries = append(entries, map[string]any{"ip": ip, "aliases": []any{host}})
	}
	network["extraHostEntries"] = entries

	// cluster.controlPlane.endpoint: the stable name, source of truth.
	cluster := childMap(first, "cluster")
	cp := childMap(cluster, "controlPlane")
	cp["endpoint"] = cfg.Cluster.Endpoint

	// certSANs only matter where the apiserver serves: controlplanes.
	if node.Role == "controlplane" {
		apiServer := childMap(cluster, "apiServer")
		apiServer["certSANs"] = appendUnique(apiServer["certSANs"], host)
	}

	out, err := yaml.Marshal(first)
	if err != nil {
		return "", fmt.Errorf("re-marshal first template document: %w", err)
	}
	rebuilt := append([]string{strings.TrimRight(string(out), "\n")}, docs[1:]...)
	return strings.Join(rebuilt, "\n---\n") + "\n", nil
}

// childMap returns parent[key] as a map[string]any, creating it if absent or
// nil. Lets the injector tolerate templates that omit machine.network etc.
func childMap(parent map[string]any, key string) map[string]any {
	if m, ok := parent[key].(map[string]any); ok && m != nil {
		return m
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

// appendUnique returns the existing certSANs list (any []any) with s appended
// when not already present, preserving hand-written node-specific SANs.
func appendUnique(existing any, s string) []any {
	var list []any
	if cur, ok := existing.([]any); ok {
		list = cur
		for _, v := range cur {
			if str, ok := v.(string); ok && str == s {
				return list
			}
		}
	}
	return append(list, s)
}
