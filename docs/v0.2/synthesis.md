# Synthesis: nostos-v02-provisioners (post-review refinement)

Inputs: review-critic.md, review-security.md, review-analyst.md, review-tests.md.
Output: in-place edits to proposal.md, design.md, tasks.md,
specs/provisioner/spec.md, specs/tpi-provisioning/spec.md.

## v0.2 scope (one-page)

**Goal:** `task nostos:install NODE=tp1` replaces taskfiles/turing.yml end
to end, dell01 keeps working bit-for-bit, and the orchestrator no longer
contains PXE-specific branches.

**Ships:**
1. `Provisioner` interface in `internal/provisioner/` with 5 hooks:
   `Preflight`, `Prepare`, `Boot`, `WaitMaintenance`, `Apply`. `Cleanup`
   is a separate always-called teardown. Each hook contract pinned
   (idempotency, ctx semantics, side-effect window).
2. Registry keyed by method string. Method enum is **{pxe, tpi}** only.
3. `pxe` provider: refactor of v0.1's `internal/pxe/` behind the interface.
   `Apply` is a no-op (PXE delivers config in-band via iPXE chain).
4. `tpi` provider: new. `Apply` runs `talosctl apply-config -i`.
5. `ContentionKey()` replaces `BMCKey()` — same shape, less misleading
   name; covers PXE-server contention and BMC contention uniformly.
6. Per-node flock at `nostos/state/configs/<name>.lock` held across
   Render → Apply for cross-process safety.
7. Orchestrator emits structured `Event{Phase, Kind, Message, At}` through
   one Scrubber sink seeded with resolved-secret values.
8. `nostos node install <name>` is canonical; `nostos up` is a thin alias
   that calls the same code path (deprecation note printed).
9. tp1, tp4 added to `nostos/config.yaml` with `boot.method: tpi`.

**Does NOT ship in v0.2 (deferred with reason):**

| Item | Defer to | Why |
|------|----------|-----|
| JSONL run log | v0.3 | Only consumer is `--resume`; operator can tee stderr today |
| `inventory.db` (SQLite) | v0.3 | No v0.2 reader; freezes schema with no usage pressure |
| Drift detection / `nostos diff` | v0.3 | Requires YAML-aware secret-subtree masking; non-trivial |
| `nostos doctor` stub + catalog | v0.4 | Stub adds noise; real catalog needs implementations |
| `--parallel` flag | v0.3 | Locks ship internally; flag stays absent until tested on real HW |
| `redfish`, `proxmox`, `usb`, `rpi-imager` providers + enum entries | v0.3+ | No implementation pressure; reserve schema bytes prematurely |
| `vm-pc01`, `pc01` in config | v0.3 (proxmox/usb) | No provider for them yet |
| Daemon split, Homebrew, vendored iPXE | v1.0 | Unscoped |

## What I cut (and why)

- **Six-method enum → {pxe, tpi}**: critic 1.1, analyst §1.5/§7,
  security §11. Reserving config shapes for unimplemented providers
  bakes guesses into the public schema.
- **JSONL run log + Tee + redaction lint test (tasks 1.5, 1.6)**: critic
  1.2, security §6. Speculative generality; the lint catches synthetic
  corpora only. v0.2 keeps in-memory events; v0.3 adds the log when
  `--resume` actually consumes it.
- **`inventory.db` design (D6)**: critic 1.3. Frozen schema with no
  v0.2 reader. Removed from design.md.
- **Doctor catalog (D8) + `nostos doctor`/`nostos diff` stubs (tasks
  5.5)**: critic 1.4, security stubs add no value.
- **`NodeView` value type + `ViewFrom` (tasks 1.3)**: critic 2.4. It's a
  duplicate of `*config.Node`. Providers import `internal/config` directly
  (read-only); decoupling-via-interface is achieved by the hook signatures.
- **`--parallel` hidden flag (tasks 5.3)**: critic 6.2. Locks ship; flag
  doesn't.
- **`vm-pc01` with `boot.method: proxmox` in config (tasks 2.3)**:
  critic 6.1, security §11. Validator now fails closed on unimplemented
  methods; vm-pc01 returns to `taskfiles/talos.yml` until v0.3.
- **`redact.Strings()` lint test (tasks 1.6)**: replaced by runtime
  Scrubber at the EventEmitter sink (security §6).
- **v0.4/v1.0 "Roadmap" prose in design.md D11**: trimmed to one
  sentence pointer; speculative.

## What I tightened (and how)

### Interface contracts (critic 2.1–2.5, security §12, tests §1)

- **Event struct, not (kind, message)**: `Event{Phase, Kind, Message,
  At}`. Phase enum pinned: `preflight|prepare|boot|wait|apply|bootstrap|
  ready|error|cleanup`.
- **Cleanup**: receives a fresh context derived from `context.Background`
  with its own 60s timeout; re-acquires its `ContentionKey` if non-empty
  before issuing destructive teardown commands. Idempotent. Always called.
- **`ContentionKey(node) string`** replaces `BMCKey`. Generalized: covers
  Turing Pi BMC, future Redfish single-resource locks, and the PXE
  server (PXE provider returns `"pxe:server"`, not empty). One model,
  no orchestrator special-case.
- **Render ordering**: orchestrator runs Preflight → Render → Prepare →
  Boot → WaitMaintenance → Apply → WaitApid → Bootstrap → Cleanup.
  Render only after Preflight (no resolved secrets touch disk before
  cheap checks pass).
- **Apply is a hook**: `Provisioner.Apply(ctx, n, configPath, emit) error`.
  PXE returns nil immediately (config already delivered in iPXE chain).
  tpi runs `talosctl apply-config -i`. No double-apply; "no external
  behavior change for dell01" now actually holds.
- **WaitMaintenance success criterion**: `talosctl --insecure -n <ip>
  version` parses successfully, NOT TCP 50000 listen. Closes
  critic 4.7 / analyst §1.4.

### Security (security §1–§12)

- **Image integrity**: `nostos/config.yaml` adds `cluster.image_digests`
  map (`<schematic>/<version>/<arch>` → sha256). Operator pins; first
  download fails closed until the digest is recorded. Replaces
  hand-waved "expected sha256."
- **xz decompression**: `github.com/ulikunitz/xz` Go module. No shell
  out (security §8).
- **Subprocess hygiene**: `Commander` seam (`exec.Cmd`-shaped), env via
  `Cmd.Env` only, never argv concatenation. `OP_SESSION_*` stripped from
  child env. Stdin used where the tool accepts it.
- **`identity_file_ref` materialization**: `O_CREAT|O_EXCL` 0600 inside
  `~/.cache/nostos/secrets/<run-id>/` (0700 dir), `lstat` to refuse
  symlinks, unlinked in Cleanup even on Ctrl-C. Path is in argv but
  never the key bytes.
- **Credential schema typing**: `_ref` fields are `Ref` Go type with a
  custom YAML unmarshaller that requires `op://`, `sops://`, or `file://`
  URI prefix. `env://` prohibited for BMC creds (process-env exposure).
  Replaces the "credential-shaped value" heuristic.
- **Tailscale authkey policy**: spec requires single-use, TTL ≤ 1h,
  rotated per install run. Documented in proposal "What This Is Not"
  + security spec scenario.
- **Rendered machineconfig lifecycle**: passed via `talosctl apply-config
  -i --file <path>` where path is a 0600 temp file in
  `~/.cache/nostos/secrets/<run-id>/`. Unlinked after Apply (success
  or failure).
- **Cross-process lock**: per-node flock at
  `nostos/state/configs/<name>.lock` held Render → Apply. Concurrent
  invocations fail fast with a typed error.
- **Reinstall guard**: orchestrator queries cluster for live node
  presenting Talos identity at `nv.IP`; if Ready, refuses unless
  `--reinstall` is explicit. Default `nostos node install` prompts
  unless `--yes`.
- **`(host, slot)` uniqueness**: validator rejects two `tpi` nodes
  sharing `(boot.tpi.host, boot.tpi.slot)`.
- **`tpi` minimum version**: Preflight parses `tpi --version`; rejects
  below `1.0.0` (placeholder; pin in implementation PR).

### Glossary (analyst §1)

Added a Definitions section to design.md covering: RK1 (Turing Pi
Rockchip RK3588 SoM, arm64), Ready (apid responds to authenticated
`talosctl version`; for controlplane: + etcd healthy + kubeconfig
fetched), maintenance mode (Talos boot state with apid on TCP 50000
without TLS client-cert auth, prior to first machineconfig apply),
idempotent (per hook), boot method (config field; one of pxe|tpi),
Provisioner (Go interface implementing one boot method).

### Acceptance signals (tests §0, §12)

Each task in tasks.md now lists a concrete observable: argv content,
env content, exit code, file mode, sha256 match, FakeClock + emit count
ceiling, regex on stderr. "Manual evidence" replaced where possible by
QEMU integration (PXE) or `Commander` mock (tpi). Real-hardware path
demoted to a structured PR template.

## Remaining open questions (recorded in design D-Open)

1. **Concrete minimum tpi version** — needs a quick test against
   current operator laptop binary. Placeholder in spec.
2. **Tailscale authkey rotation hook** — v0.2 spec requires the
   operator to rotate `op://` ref before invocation; an automated
   rotate hook is v0.3+ work. Confirm naming
   (`nostos secrets rotate --tailscale`).
3. **Cleanup retry policy on flaky BMC** — v0.2 ships single try with
   60s timeout; if real flashes show >60s power-off latency, raise
   ceiling and revisit (critic 2.3).
4. **Reinstall live-node detection** — talosctl probe vs. ARP/ICMP
   only. Prefer talosctl since it confirms "Talos at this IP" not just
   "host alive". Confirm during implementation.
5. **`nostos up` alias removal date** — pinned to v0.3 release; if
   v0.3 slips past 90 days, alias stays.

## Files changed

- proposal.md (cuts; v0.2 scope tightened)
- design.md (interface signatures, security model, glossary, deferred
  sections removed)
- tasks.md (deferred items removed; every signal now observable)
- specs/provisioner/spec.md (Apply hook, ContentionKey, Cleanup
  contract, no run-log requirement in v0.2)
- specs/tpi-provisioning/spec.md (image digest pinning, identity-file
  materialization, talosctl probe, reinstall guard, `(host, slot)`
  uniqueness)
