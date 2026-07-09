# Brief: nostos-v02-provisioners

## Goal
Draft an OpenSpec change at openspec/changes/nostos-v02-provisioners/ that extends nostos from x86-PXE-only to every Talos node, regardless of boot method.

## Read first (in this order)
1. nostos/README.md - consumer data layout
2. .submodules/nostos/README.md - tool overview
3. openspec/changes/nostos-v01/proposal.md, design.md, tasks.md - what shipped
4. openspec/changes/nostos-v01/specs/pxe-provisioning/spec.md - current single-method shape
5. .submodules/nostos/internal/cluster/orchestrate.go - current PXE-only Install flow
6. .submodules/nostos/internal/config/config.go - current Node struct
7. taskfiles/turing.yml - broken manual RK1 flow being replaced
8. taskfiles/talos.yml - manual non-nostos worker flow being replaced
9. nostos/config.yaml - only dell01 is registered
10. talos/nodes/{tp1-192.168.68.107,tp4-192.168.68.114,vm-pc01}.yaml - workers waiting for adoption

## Hard product requirements
- New abstraction: Provisioner interface with Preflight / Prepare / Boot / WaitMaintenance / Cleanup.
- Boot methods: pxe (existing), tpi (Turing Pi RK1 BMC flash), redfish (servers w/ iLO/iDRAC), proxmox (VM API), usb (operator-driven), rpi-imager (SD card).
- nostos node install <name> becomes the single user-facing command across all methods.
- --parallel for batch installs; BMC contention serialized internally.
- Resumable installs via JSONL run logs at ~/.local/state/nostos/runs/<run-id>.jsonl.
- Inventory store at ~/.local/state/nostos/inventory.db (SQLite) with node history and drift snapshots.
- Drift detection: rendered config vs. live machineconfig over Talos API.
- Pre-flight nostos doctor covers BMC reachability, secret validity, disk size, MAC collision, version match.
- BMC creds always live behind secrets backends (op:// etc), never inline.
- Backwards-compat: existing nostos.yaml (dell01) must keep working with boot.method=pxe defaulted.
- Stay within nostoss stated non-goals - no SaaS, no phone-home, single-operator.

## Shipped binaries (v1.0 north star - v0.2 is a slice)
- nostos - operator CLI + TUI (single Go binary)
- nostos-pxe - long-running PXE/dnsmasq daemon, gRPC to nostos
- nostos-bmc - BMC client (vendored tpi + Redfish/IPMI shim)
- Vendored iPXE EFI/BIOS binaries (kill the Docker-build requirement)
- Distribution: Homebrew tap, container image, optional Talos system extension

## CLI surface (v1.0)
- cluster status, cluster upgrade --to <ver>
- node add | list | show | install | reinstall | remove
- image build | list | gc
- secrets rotate | check
- pxe up | logs
- diff <node>, apply <node>
- doctor
- All commands accept --json / --output yaml. TUI only when stdin is tty.

## Roadmap split (non-negotiable)
- v0.2 - Provisioner interface + tpi provider. Closes the RK1 reset gap. THIS is what gets implemented next.
- v0.3 - Redfish + Proxmox + drift detection + inventory.db.
- v0.4 - cluster upgrade + secrets rotation + comprehensive doctor.
- v1.0 - Stable CLI, man pages, Homebrew, hardware test matrix.

## Deliverables for THIS planner run
Write all files under openspec/changes/nostos-v02-provisioners/:

1. proposal.md - Why / What Changes / Capabilities / Impact / Non-Goals. Mirror nostos-v01/proposal.md structure.

2. design.md - Long, opinionated, codebase-grounded. Cite .submodules/nostos/internal/... files and lines. Required sections:
   - Provisioner abstraction (Go interface, NodeView, registry)
   - Orchestrator reshape (how internal/cluster/orchestrate.go::Install becomes provisioner-agnostic)
   - Per-method designs: pxe, tpi, redfish, proxmox, usb, rpi-imager
   - Concurrency + BMC contention model for --parallel
   - Resumability via JSONL run logs
   - Inventory schema (SQLite tables, drift snapshot format)
   - Security model (trust boundaries, BMC creds, first-boot insecure window)
   - Doctor checks catalog
   - Backwards-compat + migration plan
   - Testing strategy
   - Roadmap with v0.2 / v0.3 / v0.4 / v1.0 scope split
   - Open questions (mark with QUESTION)

3. tasks.md - Numbered checkbox tasks. Sections:
   1. Provisioner package skeleton
   2. Config schema v2 (additive)
   3. tpi provider (v0.2 deliverable)
   4. Orchestrator refactor to use Provisioner
   5. CLI: nostos node install branches on boot.method
   6. Tests
   7. Deferred to v0.3+ (redfish, proxmox, usb, rpi-imager, inventory.db, drift, doctor, daemons)
   For each task: file path(s), brief description, acceptance signal.

4. specs/provisioner/spec.md - Capability spec for the Provisioner interface.
5. specs/tpi-provisioning/spec.md - Capability spec for the Turing Pi BMC provider.
6. .openspec.yaml - Same shape as nostos-v01/.openspec.yaml.

## Constraints on YOU as planner
- Read every file in Read first before writing. Cite specific paths/lines.
- Do NOT write code. Specs and plans only.
- Do NOT modify anything outside openspec/changes/nostos-v02-provisioners/.
- Mark uncertainties with QUESTION rather than guessing.
- After writing, list the files you created and one-line summary of each.
- After writing, list 5 questions youd want a critic + security-reviewer to focus on.
