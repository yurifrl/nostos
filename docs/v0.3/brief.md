# Brief: nostos v0.3 — bugs, hygiene, AI-friendly CLI, dashboard TUI, guide

## Goal
Plan a v0.3 release that closes the bug list from v0.2, hardens the CLI for AI/headless use, ships a Bubble Tea v2 dashboard that doubles as live documentation, and writes the canonical PXE+TPI operator guide.

## Read first
- v0.2/nostos-v02.html (full v0.2 reference)
- /Users/yuri/Workdir/Yuri/home-systems/openspec/changes/nostos-v02-provisioners/{proposal,design,tasks}.md
- /Users/yuri/Workdir/Yuri/home-systems/.submodules/nostos/internal/cluster/orchestrate.go
- /Users/yuri/Workdir/Yuri/home-systems/.submodules/nostos/internal/cli/*.go
- /Users/yuri/Workdir/Yuri/home-systems/.submodules/nostos/internal/provisioner/{tpi,pxe}/
- /Users/yuri/Workdir/Yuri/home-systems/.submodules/nostos/internal/secrets/tailscale.go
- /Users/yuri/Workdir/Yuri/home-systems/taskfiles/turing.yml (legacy to retire)
- /Users/yuri/Workdir/Yuri/home-systems/taskfiles/talos.yml
- /Users/yuri/.agents/skills/ai-friendly-cli/SKILL.md (8 principles to apply)
- /Users/yuri/.agents/skills/charm-stack/SKILL.md (Bubble Tea v2 patterns)
- existing nostos charm imports already in go.mod (charm.land/bubbletea/v2 etc)

## Scope of v0.3 (5 streams)

### Stream A — Bug fixes from v0.2
- **A1** Bug #1: BootTimeout < RK1 boot time. Bump default to 30 min for method=tpi, OR honor a per-provisioner deadline from `Provisioner.MaxWaitMaintenance() time.Duration`. Acceptance: tp1 install end-to-end without manual `talosctl apply-config --insecure`.
- **A2** Bug #5: Image-cache TOFU race. Currently we record digest after we trust the file. Rework: stream-hash on download, record digest at successful close, never trust an unrecorded digest from disk on subsequent runs. Acceptance: kill -9 mid-download leaves nothing on disk, no half-written digests.json.
- **A3** Bug #6: tpi `Device not configured (os error 6)` is misleading. nostos should pre-flight the BMC reachability + auth via a small TCP+HTTP probe and surface a clear error: "BMC at <host> unreachable" / "BMC auth failed" / "BMC version too old". Acceptance: with bogus host, error names the real issue, not the OS errno.
- **A4** [bonus] Bug #2 (`.yaml` empty filename) was fixed in v0.2 but write a regression test pinning the filename for tpi nodes.

### Stream B — Hygiene + cleanup
- **B1** Replace taskfiles/turing.yml recipes with thin nostos wrappers:
  - `task nostos:install NODE=<name>` → shells `./.bin/nostos node install <name>`
  - Old `task turing:flash`, `download`, `install-talos` print a deprecation message and exit 1 (NOT silent wrappers — old recipes don't go through new secrets pipeline).
- **B2** Same treatment for taskfiles/talos.yml worker recipes (`apply` etc.) where nostos has equivalent.
- **B3** `kubectl delete node talos-76w-r75` (zombie tp1 entry from before v0.2)
- **B4** Tailscale cleanup: list devices via OAuth API, identify offline >7d, propose deletion. Ship as `nostos cluster cleanup --dry-run` first.

### Stream C — AI-friendly CLI hardening (apply 8 principles from skill)
- **C1** Add `--output json` to ALL commands (currently only some). NDJSON for list operations.
- **C2** Add `nostos schema [method]` subcommand returning JSON of every command's flags/args/types/required/enum-values.
- **C3** Field masks: `--fields=id,ip,role` for list/show. Reduces token cost.
- **C4** `--dry-run` on every mutation: install, apply, secrets keys revoke, cluster cleanup. Output is the planned actions as JSON.
- **C5** Structured errors: JSON on stdout `{error, code, message, details}`. Hints/prose on stderr. Exit codes documented.
- **C6** Input hardening: reject control chars in node names; reject path traversal in --config; reject embedded query params in op:// refs.
- **C7** AGENTS.md inside .submodules/nostos/ documenting non-obvious invariants:
  - "Always pass --reinstall when re-flashing, never delete config first"
  - "Always run `nostos secrets test tailscale` before `node install` after editing secrets config"
  - "Run `nostos doctor` before any install on a new machine"
  - Required sequences, exit codes, idempotency guarantees
- **C8** [DEFER to v0.4] MCP server surface — same business logic, JSON-RPC over stdio.

### Stream D — `nostos dashboard` TUI (Bubble Tea v2)
The big one. A live-updating, interactive dashboard that doubles as **documentation as a program**. Goal: someone who has never touched this lab can run `nostos dashboard` and be guided to "all green" without reading any markdown.

#### D1. Layout
Split-pane Charm Bubble Tea v2 model:
```
┌─[ talos-default ]──────────────────────────────────────────[ q quit · ? help ]─┐
│ Cluster   ●●●●●  4/4 reachable  · etcd quorum OK  · k8s api ok  · TS 4 online  │
├──────────────────────────────────────────────────────────────────────────────────┤
│ ▸ dell01    100.96.13.49     Ready    controlplane  v1.10.3  Talos  amd64       │
│ ▸ tp1       100.109.122.37   Ready    worker        v1.10.3  Talos  arm64       │
│ ▸ tp4       100.112.10.120   Ready    worker        v1.10.3  Talos  arm64       │
│ ▸ pc01      192.168.68.104   ?        worker        ?        Talos  amd64+gpu   │
│ ✗ talos-76w-r75              NotReady (zombie - press d to delete)              │
│ ? 192.168.68.250             unknown — press n to name                          │
├──[ checks ]──────────────────────────────────────────────────────────────────────┤
│ ✓ Tailscale OAuth backend reachable                                              │
│ ✓ image_digests recorded for all schematics                                      │
│ ✓ ArgoCD applications healthy (12/12 Synced)                                     │
│ ⚠ Talos v1.10.3 is 2 minor versions behind upstream v1.12.0                      │
└──────────────────────────────────────────────────────────────────────────────────┘
[i]dentify  [n]ame  [h]ide  [r]einstall  [d]elete  [s]etup-info  [u]pgrade  [/]search
```

#### D2. Discovery layer
- ARP sweep + ICMP fan-out on the configured /24 (concurrency cap 32)
- mDNS/zeroconf scan for `_workstation._tcp` + `_smb._tcp` + Tailscale advertisements
- Talos maintenance API probe (TCP 50000) on every IP
- Tailscale device list via OAuth (already wired)
- ArgoCD `Application` list via k8s API
- BMC discovery: turingpi.local, common Redfish paths
- Output: `Device{IP, MAC, Hostname?, Tailscale?, Talos?, BMCRole?, Discovered_at}`

#### D3. Match layer
- Device → Node binding by MAC, then IP, then Tailscale-100.x address
- Three buckets: `known` (in config.yaml, healthy), `orphan` (in config but missing), `unknown` (on net but not in config)
- Hidden devices stored in `~/.config/nostos/dashboard.toml`

#### D4. Health checks (each renders as ✓ / ⚠ / ✗ / ?):
- Cluster level
  - etcd quorum (talosctl etcd members on controlplane)
  - k8s API reachable from operator laptop with current kubeconfig
  - All nodes Ready
  - All Tailscale nodes online
  - ArgoCD apps Synced + Healthy
- Per-node
  - ICMP up
  - Talos apid up
  - Talos version matches cluster.talos_version
  - kubelet Ready (via kubectl get node)
  - Tailscale registered (100.x address shows up in TS API)
  - schematic_id matches config
- Per-app (ArgoCD)
  - Application Synced/Healthy
  - Helm chart version vs upstream

#### D5. Diff with internet
- Cache last-known versions in `~/.cache/nostos/upstream-versions.json`
- Background refresh (24h TTL) of:
  - Talos releases via factory.talos.dev/versions
  - Helm charts via OCI registry HEAD
  - Container image tags via registry HEAD
- Render: `Talos v1.10.3 ⟶ v1.12.0 (2 minor behind, security CVE-2026-XXX in changelog)`

#### D6. Actions (keybindings on selected row)
- `i` Identify: 
  - For RK1: `tpi uart set -n <slot> --cmd "echo IDENTIFY"` (not very visible) OR `tpi power reset -n <slot>` with confirm (more visible)
  - For x86 PXE: `talosctl reboot` or no-op-ping (just blink the network LED via traffic burst)
  - Best: have a uniform `nostos node identify <name>` that picks the most visible action available per provisioner
- `n` Name unknown device: prompt for node name + role, write a stub config.yaml entry, prompt for template choice
- `h` Hide: add to `dashboard.toml` `hidden_devices` list
- `r` Reinstall: confirm prompt → `nostos node install --reinstall`
- `d` Delete: for k8s zombies, prompt → `kubectl delete node`. For Tailscale stale, prompt → `nostos secrets keys revoke + tailnet device delete`
- `s` Setup info: render Markdown-style page for selected device with:
  - Hardware: vendor/model/serial (from config or last probe)
  - BIOS: required settings (UEFI mode, secure boot off, PXE boot order, SATA mode)
  - BMC: how to reach, default creds rotation
  - Recovery: "if this dies, do these N things"
- `u` Upgrade: open the diff view, propose `nostos cluster upgrade --to <ver>` (deferred command, currently shows preview)
- `/` Search: filter devices by name/IP/role
- `?` Help: keybinding cheatsheet

#### D7. "All green" mode
Top-bar shows aggregate state. Three states:
- ✅ ALL GREEN — every check passed, no orphans, no version drift > minor
- ⚠ DEGRADED — at least one warning (version behind, hidden orphan, etc.)
- ✗ BROKEN — at least one failed check (node down, etcd unhealthy, etc.)

Press `g` (or click) to expand to a guided checklist with "fix it" buttons that dispatch the right `nostos` subcommand.

#### D8. Living documentation
- Each device's "setup-info" panel is content from `nostos/docs/<vendor>-<model>.md` files (operator-authored Markdown), rendered in TUI with Lipgloss.
- Default content shipped for: dell-optiplex-3080m, turing-rk1, generic-amd64, raspberry-pi-5.
- Operator can `nostos docs edit <vendor>-<model>` to customize.

#### D9. Implementation notes
- Bubble Tea v2 + Lipgloss v2 + Bubbles v2 (table, viewport, textinput, help, spinner)
- Use `tea.WithAltScreen` 
- Background commands via `tea.Cmd` returning typed messages
- Key polling at 200ms, network checks at 5s/30s/5min intervals (tiered)
- Snapshot to `~/.cache/nostos/dashboard-state.json` for fast cold-start
- Headless mode: `nostos dashboard --once --output json` runs all checks once and exits with the structured snapshot — for cron use

### Stream E — Comprehensive PXE+TPI guide
A single Markdown file at `/Users/yuri/Workdir/Yuri/home-systems/docs/nostos-guide.md` (NOT in submodule), opinionated, written for an operator who has the hardware in front of them and wants Talos running.

#### E1. Sections
- **0. What this gets you** — a Talos cluster with N nodes, kubelet Ready, Tailscale + ArgoCD ready
- **1. Hardware checklist** — what to buy / borrow, BIOS settings per platform, network requirements
- **2. First-time setup** — `nostos init`, `nostos.yaml` walkthrough, secrets setup (1Password, OAuth)
- **3. The PXE flow** — what happens at every step from `nostos node install dell01` to Ready
- **4. The TPI flow** — same but for RK1, including BMC creds
- **5. Per-vendor playbooks** — Dell OptiPlex 3080M, Turing RK1, generic x86 NUC, Raspberry Pi 5
- **6. Tailscale OAuth setup** — full screenshots flow, ACL changes, ownership tags
- **7. Recovery scenarios** — node won't boot, BMC unreachable, etcd quorum lost, Tailscale revoked all keys
- **8. The dashboard** — how `nostos dashboard` answers most of "what's broken" without reading this guide
- **9. Reference** — full CLI surface, config schema, exit codes, error catalogue

#### E2. Style requirements
- Every command shown is copy-pasteable
- Every error message you might see has a "what it means" explanation
- "Why" boxes explaining design choices that affect the operator
- TOC + section anchors

## Roadmap split (non-negotiable)
- **v0.3** = Streams A + B + C (mandatory) + D (dashboard MVP: discovery, health, basic actions) + E (guide v1)
- **v0.4** = MCP server surface; advanced dashboard actions (guided fix flows, upgrade dispatcher)
- **v1.0** = Stable CLI, man pages, Homebrew tap, 6+ vendor playbooks

## Deliverables for THIS planner run
Write to `/Users/yuri/Workdir/Yuri/home-systems/openspec/changes/nostos-v03-dashboard-and-hygiene/`:

1. `proposal.md` — Why / What Changes / Capabilities / Impact / Non-Goals (mirror nostos-v02-provisioners/proposal.md style)
2. `design.md` — Long, opinionated, codebase-grounded. Cite files. Required sections per Stream A-E above plus:
   - Decision: per-provisioner timeout vs cluster-wide (Stream A)
   - Decision: TOFU race remediation strategy (atomic-temp + rename, or hash-on-read)
   - Decision: schema subcommand vs --describe flag (Stream C2)
   - Decision: where dashboard.toml lives (XDG vs nostos-state)
   - Decision: dashboard checks pluggable vs hardcoded
   - Per-vendor playbook structure
   - Open questions (Q1, Q2... format)
3. `tasks.md` — Numbered checkboxed tasks per stream:
   - Section 1 (A): bug fixes
   - Section 2 (B): hygiene
   - Section 3 (C): AI-friendly CLI
   - Section 4 (D): TUI dashboard MVP
   - Section 5 (E): guide
   - Section 6: tests
   - Section 7: deferred to v0.4+
4. `specs/dashboard/spec.md` — Dashboard capability spec (lifecycle, refresh tiers, action contract)
5. `specs/cli-machine-output/spec.md` — Capability spec for AI-friendly outputs (json, schema, dry-run, structured errors)
6. `.openspec.yaml`

After writing, also write a one-page summary at `v0.3/summary.md` (50-80 lines) the user can read in 2 minutes.

## Constraints on YOU as planner
- Read every file in "Read first" before writing.
- Cite specific paths/lines.
- Do NOT write code in this run. Specs and plans only.
- Mark uncertainties with QUESTION.
- After writing, list the files you created and 5 questions for critic + security-reviewer + UX-reviewer + test-engineer to focus on (Stream D needs UX review specifically).
