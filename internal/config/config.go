// Package config parses and validates the consumer's config.yaml.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Cluster holds cluster-level settings.
type Cluster struct {
	Name         string `yaml:"name"          validate:"required"`
	Endpoint     string `yaml:"endpoint"      validate:"required,startswith=https://"`
	TalosVersion string `yaml:"talos_version" validate:"required"`
	SchematicID  string `yaml:"schematic_id"  validate:"required,len=64"`
	// ImageDigests pins sha256 of factory.talos.dev image artifacts.
	// Key format: "<schematic>/<version>/<arch>". Value: "sha256:<hex>".
	ImageDigests map[string]string `yaml:"image_digests,omitempty"`
	// TailscaleOperator is the Tailscale hostname of the in-cluster operator
	// running the API server proxy. When set, `nostos kubeconfig` (and
	// `bootstrap`) also add a remote context pointing at the proxy over the
	// tailnet, alongside the LAN context. Empty disables this entirely.
	TailscaleOperator string `yaml:"tailscale_operator,omitempty"`
}

// OnepasswordConfig is populated when secrets.backend == "onepassword".
type OnepasswordConfig struct {
	Account string `yaml:"account" validate:"required"`
	Vault   string `yaml:"vault"   validate:"required"`
}

// SopsConfig is populated when secrets.backend == "sops".
type SopsConfig struct {
	AgeKeyFile string `yaml:"age_key_file,omitempty"`
}

// TailscaleConfig is populated when the operator wants the `tailscale://`
// URI scheme registered. Optional: when absent, templates that reference
// tailscale:// fail loud at render time.
type TailscaleConfig struct {
	OAuthClientIDRef     Ref      `yaml:"oauth_client_id_ref"     validate:"required"`
	OAuthClientSecretRef Ref      `yaml:"oauth_client_secret_ref" validate:"required"`
	Tags                 []string `yaml:"tags,omitempty"`
	ExpirySeconds        int      `yaml:"expiry,omitempty"`
	Reusable             bool     `yaml:"reusable,omitempty"`
	Ephemeral            bool     `yaml:"ephemeral,omitempty"`
	Preauthorized        bool     `yaml:"preauthorized,omitempty"`
	Description          string   `yaml:"description,omitempty"`
	Tailnet              string   `yaml:"tailnet,omitempty"`
}

// Secrets selects the active backend for URI resolution.
type Secrets struct {
	Backend     string             `yaml:"backend" validate:"required,oneof=onepassword sops env file"`
	Onepassword *OnepasswordConfig `yaml:"onepassword,omitempty"`
	Sops        *SopsConfig        `yaml:"sops,omitempty"`
	Tailscale   *TailscaleConfig   `yaml:"tailscale,omitempty"`
}

// Ref is a typed-string for secret references. Allowed schemes are
// op://, sops://, file://. env:// is rejected for credential refs.
type Ref string

// UnmarshalYAML enforces the ref scheme allowlist.
func (r *Ref) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*r = ""
		return nil
	}
	switch {
	case strings.HasPrefix(s, "op://"),
		strings.HasPrefix(s, "sops://"),
		strings.HasPrefix(s, "file://"):
		*r = Ref(s)
		return nil
	case strings.HasPrefix(s, "env://"):
		return fmt.Errorf("env:// scheme not allowed for credential refs (got %q)", s)
	default:
		return fmt.Errorf("ref %q: must start with op://, sops://, or file://", s)
	}
}

// String returns the raw URI.
func (r Ref) String() string { return string(r) }

// TPIBoot describes Turing-Pi BMC settings for a node.
type TPIBoot struct {
	Host            string `yaml:"host"             validate:"required"`
	Slot            int    `yaml:"slot"             validate:"required,min=1,max=4"`
	UsernameRef     Ref    `yaml:"username_ref,omitempty"`
	PasswordRef     Ref    `yaml:"password_ref,omitempty"`
	IdentityFileRef Ref    `yaml:"identity_file_ref,omitempty"`
}

// OSConfig is the node-level choice of which operating system nostos installs.
// It is orthogonal to the boot method (pxe/tpi/flash) — every transport reads
// it. Absent block defaults to "talos".
type OSConfig struct {
	// Name selects the OS: "talos" (default) | "proxmox".
	Name string `yaml:"name,omitempty" validate:"omitempty,oneof=talos proxmox"`
	// Version is required when Name != talos. Either "latest" (nostos resolves
	// the newest release) or a pinned release (e.g. "8.3-1").
	Version string `yaml:"version,omitempty"`
}

// PXEBoot is the REMOVED transitional boot.pxe block. It is kept only so a
// config still carrying it fails loud with a migration hint (yaml.v3 silently
// ignores truly-unknown fields). Use the node-level os: block instead.
type PXEBoot struct {
	Target  string `yaml:"target,omitempty"`
	Version string `yaml:"version,omitempty"`
}

// proxmoxVersionRE matches a pinned Proxmox version like "8.3-1".
var proxmoxVersionRE = regexp.MustCompile(`^\d+\.\d+-\d+$`)

// Boot selects the install method for a node. Default Method is "pxe".
type Boot struct {
	Method string   `yaml:"method,omitempty" validate:"omitempty,oneof=pxe tpi flash"`
	TPI    *TPIBoot `yaml:"tpi,omitempty"`
	// PXE is REMOVED; retained only to detect+reject legacy configs.
	PXE *PXEBoot `yaml:"pxe,omitempty"`
}

// OSName returns the node's OS name, defaulting to "talos".
func (n Node) OSName() string {
	if n.OS == nil || n.OS.Name == "" {
		return "talos"
	}
	return n.OS.Name
}

// Node is one declared bare-metal or VM node.
type Node struct {
	MAC         string `yaml:"mac,omitempty" validate:"omitempty,mac"`
	IP          string `yaml:"ip"           validate:"required,ip4_addr"`
	// TailscaleIP is the node's stable tailnet address (100.64.0.0/10). Set it
	// on control planes so off-subnet peers (e.g. an offsite node) can still
	// reach the HA control-plane endpoint when the LAN IP isn't routable.
	TailscaleIP string `yaml:"tailscale_ip,omitempty" validate:"omitempty,ip4_addr"`
	Role        string `yaml:"role"         validate:"required,oneof=controlplane worker"`
	Arch        string `yaml:"arch"         validate:"required,oneof=amd64 arm64"`
	InstallDisk string `yaml:"install_disk" validate:"required,startswith=/dev/"`
	// Template is the Talos machineconfig template. Required for talos-target
	// nodes; not needed for non-talos PXE targets (e.g. proxmox), which don't
	// render a Talos machineconfig. Conditional requirement is enforced in
	// Validate() rather than a struct tag.
	Template string `yaml:"template,omitempty"`
	Boot     Boot   `yaml:"boot,omitempty"`
	// OS selects which operating system nostos installs (default talos). It is
	// orthogonal to Boot.Method; pxe/flash both read it.
	OS *OSConfig `yaml:"os,omitempty"`
	// SchematicID overrides Cluster.SchematicID for this node when set.
	// Required for SBCs that need a different overlay than the cluster default
	// (e.g. Turing RK1 needs siderolabs/sbc-rockchip overlay; x86 nodes don't).
	SchematicID string `yaml:"schematic_id,omitempty" validate:"omitempty,len=64"`
	// Overlay is the Talos imager overlay name for SBC-specific image layout
	// (e.g. "rpi_generic", "turing_rk1"). When set, ship/build commands know
	// to fetch SBC-specific firmware (start4.elf for rpi_generic) and to
	// produce SBC-specific image artifacts (EEPROM recovery partition for
	// rpi_generic). Empty = generic metal image.
	Overlay string `yaml:"overlay,omitempty" validate:"omitempty,oneof=rpi_generic turing_rk1"`
	// Serial is the SBC hardware serial number used for per-device TFTP
	// path matching during PXE boot (RPi 4 only). Optional. 8 hex chars,
	// from `/proc/cpuinfo` or boot screen. Currently informational only;
	// reserved for future RPi PXE/TFTP support.
	Serial string `yaml:"serial,omitempty" validate:"omitempty,len=8,hexadecimal"`
}

// EffectiveSchematic returns the node's SchematicID when set, otherwise the
// cluster default. Centralizes the override rule so callers don't pick wrong.
func (n Node) EffectiveSchematic(cluster Cluster) string {
	if n.SchematicID != "" {
		return n.SchematicID
	}
	return cluster.SchematicID
}

// MACHyphen returns the MAC in iPXE ${mac:hexhyp} form: d0-94-66-d9-eb-a5.
func (n Node) MACHyphen() string {
	return strings.ReplaceAll(strings.ToLower(n.MAC), ":", "-")
}

// EndpointHost returns the hostname portion of the cluster endpoint URL
// (e.g. "api.k8s.lan" from "https://api.k8s.lan:6443"). Used to derive the
// /etc/hosts alias and apiserver certSAN the endpoint injector emits.
func (c Cluster) EndpointHost() (string, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return "", fmt.Errorf("parse cluster endpoint %q: %w", c.Endpoint, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("cluster endpoint %q has no host", c.Endpoint)
	}
	return host, nil
}

// ControlPlaneEndpointAddrs returns every address the HA control-plane endpoint
// should resolve to: each controlplane node's LAN IP followed by its Tailscale
// IP (when set), ordered by node name for determinism. A node's resolver tries
// them in order and connects to the first reachable apiserver.
// ponytail: relies on dead hosts failing fast (no-route/refused), not silent
// black-hole timeouts; true on a LAN. Swap for a VIP if all CPs go same-L2.
func (c *Config) ControlPlaneEndpointAddrs() []string {
	names := make([]string, 0, len(c.Nodes))
	for name, n := range c.Nodes {
		if n.Role == "controlplane" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	addrs := make([]string, 0, len(names)*2)
	for _, name := range names {
		n := c.Nodes[name]
		addrs = append(addrs, n.IP)
		if n.TailscaleIP != "" {
			addrs = append(addrs, n.TailscaleIP)
		}
	}
	return addrs
}

// Config is the root document parsed from config.yaml.
type Config struct {
	Cluster Cluster `yaml:"cluster" validate:"required"`
	Secrets Secrets `yaml:"secrets" validate:"required"`
	// Bootstrap is the optional cluster-bootstrap tier. nil => legacy behavior
	// (templates own their inline manifests; render synthesizes nothing).
	Bootstrap *Bootstrap      `yaml:"bootstrap,omitempty"`
	Nodes     map[string]Node `yaml:"nodes,omitempty" validate:"dive"`
	// Images are guest-VM install ISOs (build/publish/sign), keyed by name.
	// Everything machine-specific (bucket, object, project, creds) lives here so
	// the code never hardcodes a machine identity.
	Images map[string]Image `yaml:"images,omitempty" validate:"dive"`
}

// Image describes a guest-VM install ISO: how to build it, where to publish it,
// and the credentials to do so. Resolved by name from Config.Images.
type Image struct {
	Build          ImageBuild `yaml:"build"           validate:"required"`
	Store          ImageStore `yaml:"store"           validate:"required"`
	// CredentialsRef resolves (via the secrets backend) to the object-store
	// service-account key JSON used for upload + signing.
	CredentialsRef Ref `yaml:"credentials_ref" validate:"required"`
}

// ImageBuild holds the inputs the container build needs to assemble the ISO.
type ImageBuild struct {
	UUPID        string `yaml:"uup_id"        validate:"required"`
	Edition      string `yaml:"edition"       validate:"required"`
	DriverSource string `yaml:"driver_source" validate:"required,url"`
	// AnswerFile is a path (relative to the config root) to autounattend.xml.
	AnswerFile   string `yaml:"answer_file"   validate:"required"`
}

// ImageStore is the object-store target for the built ISO. Bucket is a secret
// ref (op://...) so the bucket name never appears as a literal in (public)
// config; object is a plain, non-sensitive file name.
type ImageStore struct {
	Bucket Ref    `yaml:"bucket" validate:"required"`
	Object string `yaml:"object" validate:"required"`
}

// ImageByName resolves a named image entry, erroring clearly when absent.
func (c *Config) ImageByName(name string) (Image, error) {
	img, ok := c.Images[name]
	if !ok {
		names := make([]string, 0, len(c.Images))
		for n := range c.Images {
			names = append(names, n)
		}
		return Image{}, fmt.Errorf("no image %q in config (known: %v)", name, names)
	}
	return img, nil
}

var nodeNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Load reads, parses, and validates a config.yaml. Returns a human-readable
// error on any validation failure.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Normalize before validation: lowercase MAC, strip surrounding whitespace,
	// default Boot.Method to "pxe" when omitted.
	for name, node := range cfg.Nodes {
		node.MAC = strings.ToLower(strings.TrimSpace(node.MAC))
		if node.Boot.Method == "" {
			node.Boot.Method = "pxe"
		}
		cfg.Nodes[name] = node
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate runs schema + semantic checks on an already-unmarshaled Config.
func (c *Config) Validate() error {
	v := validator.New(validator.WithRequiredStructEnabled())

	if err := v.Struct(c); err != nil {
		return translate(err)
	}

	// Backend-specific config required.
	if c.Secrets.Backend == "onepassword" && c.Secrets.Onepassword == nil {
		return fmt.Errorf("secrets.backend=onepassword requires secrets.onepassword block")
	}

	// Node names: kebab-case.
	for name := range c.Nodes {
		if !nodeNameRE.MatchString(name) {
			return fmt.Errorf(
				"invalid node name %q: must start with a lowercase letter and contain only lowercase letters, digits, and hyphens",
				name,
			)
		}
	}

	// Image names: kebab-case (same rule as nodes).
	for name := range c.Images {
		if !nodeNameRE.MatchString(name) {
			return fmt.Errorf(
				"invalid image name %q: must start with a lowercase letter and contain only lowercase letters, digits, and hyphens",
				name,
			)
		}
	}

	// No duplicate MACs across nodes (empty MACs ignored).
	macToNames := map[string][]string{}
	for name, node := range c.Nodes {
		if node.MAC == "" {
			continue
		}
		macToNames[node.MAC] = append(macToNames[node.MAC], name)
	}
	var dupes []string
	for mac, names := range macToNames {
		if len(names) > 1 {
			sort.Strings(names)
			dupes = append(dupes, fmt.Sprintf("  %s: %s", mac, strings.Join(names, ", ")))
		}
	}
	if len(dupes) > 0 {
		sort.Strings(dupes)
		return fmt.Errorf("duplicate MAC addresses across nodes:\n%s", strings.Join(dupes, "\n"))
	}

	// IPv4 sanity is already covered by validator, but parse once to catch weird cases.
	for name, node := range c.Nodes {
		if net.ParseIP(node.IP) == nil {
			return fmt.Errorf("node %s has invalid IP %q", name, node.IP)
		}
	}

	// Boot-method-specific validation.
	type hostSlot struct {
		host string
		slot int
	}
	hostSlotToNames := map[hostSlot][]string{}
	for name, node := range c.Nodes {
		switch node.Boot.Method {
		case "", "pxe":
			// no extra validation
		case "tpi":
			if node.Boot.TPI == nil {
				return fmt.Errorf("node %s: boot.method=tpi requires boot.tpi block", name)
			}
			tpi := node.Boot.TPI
			// Credential refs are all optional; when absent the tpi provider
			// falls back to the tpi CLI's cached token / interactive prompt.
			key := hostSlot{tpi.Host, tpi.Slot}
			hostSlotToNames[key] = append(hostSlotToNames[key], name)
		case "flash":
			// flash needs no extra config: the image is produced offline by
			// `nostos flash` and the operator boots the node by hand. The
			// machineconfig is applied insecurely once the node reaches
			// maintenance mode (see internal/provisioner/flash).
		}
	}

	// PXE method requires MAC (for iPXE matching). tpi method does not.
	for name, node := range c.Nodes {
		method := node.Boot.Method
		if method == "" {
			method = "pxe"
		}
		if method == "pxe" && node.MAC == "" {
			return fmt.Errorf("node %s: boot.method=pxe requires mac", name)
		}
	}

	// OS selection rules. Talos targets need a Talos template; non-talos
	// targets need a version (latest | pinned) and don't need a template. The
	// removed boot.pxe block is rejected with a migration hint.
	for name, node := range c.Nodes {
		if node.Boot.PXE != nil {
			return fmt.Errorf("node %s: boot.pxe is removed; move the OS choice to a node-level os: block (e.g. os: {name: proxmox, version: latest})", name)
		}
		osName := node.OSName()
		if osName == "talos" {
			if node.Template == "" {
				return fmt.Errorf("node %s: template is required for talos nodes", name)
			}
			continue
		}
		// Non-talos OS (e.g. proxmox): version is mandatory and validated before
		// any network call.
		if node.OS == nil || node.OS.Version == "" {
			return fmt.Errorf("node %s: os.version is required when os.name=%s (use \"latest\" or a pinned release like \"8.3-1\")", name, osName)
		}
		v := node.OS.Version
		if v != "latest" && !proxmoxVersionRE.MatchString(v) {
			return fmt.Errorf("node %s: os.version %q is invalid: use \"latest\" or a pinned release matching \"<major>.<minor>-<build>\" (e.g. \"8.3-1\")", name, v)
		}
	}
	var collisions []string
	for key, names := range hostSlotToNames {
		if len(names) > 1 {
			sort.Strings(names)
			collisions = append(collisions, fmt.Sprintf("  %s slot %d: %s", key.host, key.slot, strings.Join(names, ", ")))
		}
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		return fmt.Errorf("duplicate (host, slot) across tpi-method nodes:\n%s", strings.Join(collisions, "\n"))
	}

	return nil
}

// translate converts validator's errors into a single human-readable message.
func translate(err error) error {
	if err == nil {
		return nil
	}
	valErrs, ok := err.(validator.ValidationErrors)
	if !ok {
		return err
	}
	msgs := make([]string, 0, len(valErrs))
	for _, e := range valErrs {
		msgs = append(msgs, fmt.Sprintf("  %s: %s", e.Namespace(), describeRule(e)))
	}
	return fmt.Errorf("validation failed:\n%s", strings.Join(msgs, "\n"))
}

func describeRule(e validator.FieldError) string {
	switch e.Tag() {
	case "required":
		return "is required"
	case "mac":
		return fmt.Sprintf("must be a MAC address (got %q)", e.Value())
	case "ip4_addr":
		return fmt.Sprintf("must be an IPv4 address (got %q)", e.Value())
	case "oneof":
		return fmt.Sprintf("must be one of %s (got %q)", e.Param(), e.Value())
	case "startswith":
		return fmt.Sprintf("must start with %q (got %q)", e.Param(), e.Value())
	case "len":
		return fmt.Sprintf("must be exactly %s characters (got %d)", e.Param(), len(fmt.Sprint(e.Value())))
	default:
		return fmt.Sprintf("failed %s rule", e.Tag())
	}
}
