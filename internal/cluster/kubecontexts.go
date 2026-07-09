package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/yurifrl/nostos/internal/config"
	"github.com/yurifrl/nostos/internal/paths"
	"gopkg.in/yaml.v3"
)

// kubeconfigDoc is a minimal typed view of a kubeconfig for context surgery.
type kubeconfigDoc struct {
	APIVersion     string          `yaml:"apiVersion"`
	Kind           string          `yaml:"kind"`
	Clusters       []kubeNamedItem  `yaml:"clusters"`
	Contexts       []kubeNamedItem  `yaml:"contexts"`
	Users          []kubeNamedItem  `yaml:"users"`
	CurrentContext string          `yaml:"current-context"`
	Preferences    map[string]any  `yaml:"preferences,omitempty"`
}

// kubeNamedItem is a {name, <payload>} entry. The payload key differs per
// section (cluster/context/user); we keep it as a raw node to round-trip.
type kubeNamedItem struct {
	Name    string    `yaml:"name"`
	Cluster yaml.Node `yaml:"cluster,omitempty"`
	Context yaml.Node `yaml:"context,omitempty"`
	User    yaml.Node `yaml:"user,omitempty"`
}

// GenerateContexts post-processes the fetched kubeconfig at p.Kubeconfig() so
// kubectl works from any network. It emits, sharing the fetched CA + admin
// user:
//   - one context per controlplane node at its LAN IP   (<cluster>-<node>)
//   - one tailscale context at a controlplane's tailnet IP (<cluster>-ts)
//
// The tailscale context deliberately uses the tailnet IP, not the operator
// MagicDNS hostname: an IP only needs the home tailnet reachable, whereas the
// hostname breaks the moment a different tailnet's MagicDNS is active (e.g. a
// corporate tailnet). Best-effort: a node whose cert/SAN or tailnet IP can't
// be resolved is skipped, never fatal.
func GenerateContexts(ctx context.Context, cfg *config.Config, p paths.Paths) ([]string, error) {
	path := p.Kubeconfig()
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig %s: %w", path, err)
	}
	var doc kubeconfigDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}

	// Base cluster (CA) + base user come from the current context.
	baseCtx := findContext(doc, doc.CurrentContext)
	if baseCtx == nil && len(doc.Contexts) > 0 {
		baseCtx = &doc.Contexts[0]
	}
	if baseCtx == nil {
		return nil, fmt.Errorf("kubeconfig has no contexts to derive from")
	}
	baseClusterName := scalarField(baseCtx.Context, "cluster")
	baseUserName := scalarField(baseCtx.Context, "user")
	baseCluster := findCluster(doc, baseClusterName)
	if baseCluster == nil {
		return nil, fmt.Errorf("kubeconfig base cluster %q not found", baseClusterName)
	}
	caData := scalarField(baseCluster.Cluster, "certificate-authority-data")
	if caData == "" {
		return nil, fmt.Errorf("kubeconfig base cluster has no certificate-authority-data")
	}

	clusterName := cfg.Cluster.Name
	if clusterName == "" {
		clusterName = baseClusterName
	}

	// Controlplane nodes, sorted for determinism.
	var cps []string
	for name, n := range cfg.Nodes {
		if n.Role == "controlplane" {
			cps = append(cps, name)
		}
	}
	sort.Strings(cps)

	added := []string{}
	// Per-CP LAN contexts.
	for _, name := range cps {
		n := cfg.Nodes[name]
		ctxName := clusterName + "-" + name
		server := "https://" + n.IP + ":6443"
		upsertClusterContext(&doc, ctxName, server, caData, baseUserName)
		added = append(added, ctxName)
	}

	// Tailscale context: prefer dell01's tailnet IP, else the first CP we can
	// resolve. IP-based so it survives MagicDNS/tailnet switches.
	tsIP := ""
	for _, pref := range append([]string{"dell01"}, cps...) {
		if ip := tailnetIPFor(ctx, pref); ip != "" {
			tsIP = ip
			break
		}
	}
	if tsIP != "" {
		ctxName := clusterName + "-ts"
		upsertClusterContext(&doc, ctxName, "https://"+tsIP+":6443", caData, baseUserName)
		added = append(added, ctxName)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("marshal kubeconfig: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return nil, fmt.Errorf("write kubeconfig: %w", err)
	}
	return added, nil
}

// upsertClusterContext adds (or replaces) a cluster named ctxName with the
// given server+CA, and a context of the same name binding it to userName.
func upsertClusterContext(doc *kubeconfigDoc, ctxName, server, caData, userName string) {
	clusterNode := mappingNode(map[string]string{
		"server":                     server,
		"certificate-authority-data": caData,
	})
	contextNode := mappingNode(map[string]string{
		"cluster": ctxName,
		"user":    userName,
	})

	replaced := false
	for i := range doc.Clusters {
		if doc.Clusters[i].Name == ctxName {
			doc.Clusters[i].Cluster = clusterNode
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Clusters = append(doc.Clusters, kubeNamedItem{Name: ctxName, Cluster: clusterNode})
	}
	replaced = false
	for i := range doc.Contexts {
		if doc.Contexts[i].Name == ctxName {
			doc.Contexts[i].Context = contextNode
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Contexts = append(doc.Contexts, kubeNamedItem{Name: ctxName, Context: contextNode})
	}
}

// tailnetIPFor returns the first IPv4 tailnet address of the peer/self whose
// hostname starts with namePrefix, via `tailscale status --json`. "" if not
// found or tailscale is unavailable.
func tailnetIPFor(ctx context.Context, namePrefix string) string {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return ""
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var st struct {
		Self *tsPeer            `json:"Self"`
		Peer map[string]tsPeer  `json:"Peer"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return ""
	}
	candidates := make([]tsPeer, 0, len(st.Peer)+1)
	if st.Self != nil {
		candidates = append(candidates, *st.Self)
	}
	for _, pr := range st.Peer {
		candidates = append(candidates, pr)
	}
	for _, pr := range candidates {
		if !strings.HasPrefix(strings.ToLower(pr.HostName), strings.ToLower(namePrefix)) {
			continue
		}
		for _, ip := range pr.TailscaleIPs {
			if strings.Count(ip, ".") == 3 { // IPv4
				return ip
			}
		}
	}
	return ""
}

type tsPeer struct {
	HostName     string   `json:"HostName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

// --- small yaml helpers ---

func findContext(doc kubeconfigDoc, name string) *kubeNamedItem {
	for i := range doc.Contexts {
		if doc.Contexts[i].Name == name {
			return &doc.Contexts[i]
		}
	}
	return nil
}

func findCluster(doc kubeconfigDoc, name string) *kubeNamedItem {
	for i := range doc.Clusters {
		if doc.Clusters[i].Name == name {
			return &doc.Clusters[i]
		}
	}
	return nil
}

// scalarField returns the string value of key in a mapping yaml.Node.
func scalarField(m yaml.Node, key string) string {
	if m.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1].Value
		}
	}
	return ""
}

// mappingNode builds a yaml mapping node from string key/values (sorted keys).
func mappingNode(kv map[string]string) yaml.Node {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, k := range keys {
		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv[k]})
	}
	return n
}
