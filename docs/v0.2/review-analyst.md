# Analyst Review: openspec/changes/nostos-v02-provisioners

Lens: requirements-engineering. Not a security or architecture critique
(see `review-security.md` and `review-critic.md`). Focus: ambiguous
language, hidden constraints, goal/design mismatches, scope drift,
hand-waved acceptance signals, undocumented operator knowledge, missing
definitions, terminology inconsistencies.

Files inspected: proposal.md, design.md, tasks.md,
specs/provisioner/spec.md, specs/tpi-provisioning/spec.md,
v0.2/{brief,review-critic,review-security}.md.

## 1. Missing or under-defined terms

The change set assumes a shared vocabulary that is not actually shared.
The following terms are load-bearing but undefined or defined only by
example.

### 1.1 "RK1"
Used in `proposal.md` paragraph 1 ("Turing Pi RK1 modules"),
`design.md` Context bullet ("Turing Pi RK1 ARM modules"), and
`specs/tpi-provisioning/spec.md` Requirement 1 title ("RK1 module"). The
term is never defined. A reader who has not operated a Turing Pi 2
cannot infer:
- That RK1 is a specific compute module (Rockchip RK3588-based) versus
  a class of devices.
- That `boot.method=tpi` is *not* RK1-specific — the same provider
  presumably handles RPi CM4, Jetson, and any future Turing-Pi-hosted
  module. `proposal.md` Capabilities ("`tpi-provisioning` — Turing Pi
  BMC provider for RK1 (and any other Turing-Pi-hosted) modules") is the
  only place this is hinted at, and the parenthetical contradicts the
  provider-name's apparent specificity.
- Whether arch is fixed (`design.md` D3 hard-codes `metal-arm64.raw.xz`)
  or method-derived. tasks.md 2.3 ties tp1/tp4 to `arm64`; nothing
  explains what happens for an x86 module on a Turing Pi board.

Required clarification: define "RK1" once (Turing Pi RK1 SoM, Rockchip
RK3588, arm64) and decouple it from the `tpi` method name or rename
the provider to `turingpi`/`tpi-bmc` and drop "RK1" from spec
requirement titles.

### 1.2 "Ready"
The orchestrator's terminal event is named `ready` (`design.md` D2 final
emit; `specs/provisioner/spec.md` Run-log scenario "the last line has
kind=ready"). What does Ready actually mean?
- Apid up at static IP? (post-`WaitApid`)
- Bootstrap complete? (controlplane only)
- Joined to etcd?
- Node Ready in the Kubernetes sense (`kubectl get nodes`)?

`design.md` D2 fires `emit("ready", ...)` after `Bootstrap` for
controlplane and after `WaitApid` for workers. So a worker is "ready"
without being a Kubernetes node. `tasks.md` 4.5 is silent.
`tpi-provisioning/spec.md` Happy-path scenario ends "the orchestrator
emits a final ready event" — also undefined.

This matters for verification: a scenario that asserts "kind=ready"
(`specs/provisioner/spec.md` Run-log scenario) is satisfiable by emitting
the literal string with no observable system state. The acceptance signal
is a string match, not a system-state proof. Spec needs a definition:
"Ready means apid responds to `talosctl version` over the secured
listener AND, for controlplane, etcd is healthy AND
`talos/kubeconfig` has been fetched."

### 1.3 "Drift"
Used 7+ times across `design.md` (D6, D8), `proposal.md` ("drift
snapshots"), `00-brief.md`. The closest the docs come to a definition is
`design.md` D6: "render machineconfig in-memory, fetch live machineconfig
via `talosctl get mc -o yaml`, normalize (sort keys, strip volatile
fields like timestamps), sha256 each, capture diff if differs."

What is missing:
- The list of "volatile fields" is not enumerated. Talos machineconfig
  contains generated certificates, install image SHA references,
  inline rendered secrets (Tailscale authkey), node UUID. Each is a
  guaranteed false-positive without explicit normalization rules.
- Rendered config resolves `op://` at render time; live config carries
  whatever was applied at install time. If the operator rotated a
  secret in 1Password since install, drift is a foregone conclusion.
  See `review-critic.md` §8 — analyst confirms the requirement is
  underspecified, not just over-engineered.
- "Drift detection" in `design.md` D8 doctor catalog is listed without a
  definition link; reader has to find D6.

This is v0.3 work per the roadmap, but the term leaks into v0.2
(`tasks.md` 5.5 ships a stub `nostos diff`, `proposal.md` mentions
"drift detection" in the v0.3 sketch). Pin a definition or do not use
the word in v0.2 docs.

### 1.4 "Maintenance mode"
Invoked across `proposal.md`, `design.md` D1 / D3, and
`specs/provisioner/spec.md` Req 1. Never defined. `tasks.md` 3.6 polls
TCP `nv.IP:50000`, conflating TCP-listening with maintenance-mode-ready
(see `review-critic.md` §4.7). This is a *definition* failure as much as
a probe failure. Required: define inline as "Talos boot state where apid
is exposed without TLS client cert auth on TCP 50000, prior to first
machineconfig apply" and pin the success criterion beyond TCP connect.

### 1.5 "Boot method" / "provisioner" / "provider" / "install method"
Four terms used interchangeably across `proposal.md`, `design.md` D1/D3,
`tasks.md`, and `specs/provisioner/spec.md` Req 1 ("install method" in
title, "method-specific" in body). Settle on `boot.method` (config
field) and `Provisioner` (Go interface); drop "provider".

### 1.6 "BMC"
`design.md` D7 calls the BMC LAN "semi-trusted"; the term covers Turing
Pi controller, iLO, iDRAC, generic Redfish, and (per `BMCKey`) the
Proxmox host API. Proxmox is not a BMC. Either rename `BMCKey` to
`ContentionKey` or scope BMC per provider.

### 1.7 "Idempotent"
`design.md` D1 `Prepare`: "Safe to re-run"; `tasks.md` 3.2: "skip when
sha256 matches." `Boot` is *not* documented as idempotent yet `--resume`
(D5) requires it. Define which hooks MUST be idempotent and the meaning
(no observable side effect on second call vs converges-to-same-state).

## 2. Hidden constraints

These constraints exist but are not surfaced in proposal/spec/tasks.

- **Docker on the operator workstation** is implied by `proposal.md`
  ("v1.0: vendored iPXE binaries (kill the Docker requirement from
  v0.1)") but never declared as a v0.2 constraint or Preflight check.
- **`tpi` CLI** is treated as "already on operator's laptop"
  (`proposal.md`); v0.2 actually adds it as a hard runtime dep. State it.
- **Static IPs** for tp1/tp4 are hard-coded (`tasks.md` 2.3,
  `tpi-provisioning/spec.md` happy path); the DHCP-reservation
  prerequisite is implicit, with no Preflight ARP/ICMP check.
- **Single-laptop / single-operator** is a non-goal in `proposal.md`
  but flows into shared `~/.cache/nostos/`, shared `runs/`, and a
  single `op` session — all unprotected. Encode in validator or runtime
  warning, not prose.
- **`op://` is the only tested backend**; `design.md` D7 lists four
  schemes but `tasks.md` 3.4 fixture is unspecified.
- **talosctl version coupling**: orchestrator shells out to
  `talosctl apply-config -i` / `bootstrap` with no pinned minimum
  version (cf. `review-critic.md` §7).

## 3. Mismatches between goals and design

- **"Smallest slice" vs over-built interface.** `proposal.md` para 1
  promises "the smallest slice that closes the most painful gap (RK1
  reset)." Actual: 6-method enum, BMCSemaphore for absent hardware,
  JSONL log feeding a v0.3 resume, inventory.db schema, doctor
  catalog. The brief asked for the architecture sketch; the proposal
  asked for minimum slice. Pick one.
- **"No external behavior change for dell01"** (`proposal.md` Modified
  Capabilities) vs `design.md` D2 inserting `ApplyConfigInsecure` into
  the PXE flow. v0.1 PXE delivered config in-band; D2 adds a second
  apply. Stated-goal contradiction (technical race already noted in
  `review-critic.md` §3.2).
- **"Provisioner-agnostic orchestrator"** (`specs/provisioner/spec.md`
  Req 1, "no PXE-only branches") vs `design.md` D4 introducing a
  PXE-specific `pxe:server` lock the orchestrator holds. Direct
  contradiction.
- **"dell01 unchanged" vs missing-block warning.** `review-security.md`
  §11 recommends warning when `boot:` is absent — a behavior change
  for dell01. Spec commits to neither path.
- **"Replaces turing.yml end-to-end"** (`proposal.md`) vs "kept as
  deprecation wrappers for one minor release" (`design.md` D9,
  `tasks.md` 5.6). Two strategies in two docs; pin one.

## 4. Acceptance signals: verifiable vs hand-waved

### 4.1 Verifiable (good)
- `tasks.md` 1.2: "Register panics on duplicates" — unit testable.
- `tasks.md` 2.1: "existing nostos/config.yaml loads without error" —
  testable.
- `tasks.md` 3.2: "httptest server confirms download + skip-on-cached"
  — testable.
- `tasks.md` 3.5: "captured argv contains no resolved password value"
  — testable via Commander mock.
- `tasks.md` 4.4: "second blocks until first releases" — testable with
  fakes.

### 4.2 Hand-waved
- `tasks.md` 1.5: tests append-only write, not Ctrl-C completeness.
- `tasks.md` 1.6: redaction lint tests the test corpus, not runtime
  redaction (see `review-security.md` §6).
- `tasks.md` 2.5: "README includes a copy-pasteable boot.tpi block" —
  true even if wrong; should test the block parses + validates.
- `tasks.md` 3.8: "fewer than ~150 emits" — magic number with `~`.
- `tasks.md` 4.1: golden Event sequence is brittle and ignores system
  state — same kinds in same order with a missed Talos call passes.
- `tasks.md` 5.2 / 5.3: stderr string matches ("deprecated", "parallel
  installs land in v0.3") couple downstream tooling to message text.
- `tasks.md` 6.3: "manual evidence in PR description" is not an
  automatable signal; reviewer + checklist absent.

### 4.3 Unverifiable as written
- `specs/provisioner/spec.md` Run-log scenario: "last line has
  kind=ready" — `ready` is undefined (see §1.2).
- `specs/provisioner/spec.md` Cleanup scenario: "completes within 30s";
  magic number, contradicted by tpi power-off latency
  (`review-critic.md` §2.3).
- `specs/tpi-provisioning/spec.md` cache scenario: "matches the
  upstream factory.talos.dev sha256" — source of the expected SHA
  unspecified (`review-security.md` §7).
- `specs/tpi-provisioning/spec.md` "completes in under 2 seconds
  (excluding decompression)" — decompression is the prepare phase, so
  the carve-out is empty.

## 5. Undocumented operator knowledge

The docs assume the reader already knows:
- The `factory.talos.dev` URL pattern
  `<schematic>/<version>/metal-<arch>.raw.xz` (`design.md` D3, no link).
- That `tpi flash -n <slot>` is destructive without prompt; v0.1 had an
  explicit wipe queue, v0.2 tpi has implicit destruction
  (`review-critic.md` §4.10).
- That apid listens on TCP 50000 in maintenance mode (`tasks.md` 3.6
  cites the port with no reference).
- That `cluster.SchematicID` / `cluster.TalosVersion` live in
  `nostos/config.yaml` (inherited from v0.1; not redefined here).
- That iPXE chains include `talos.config=http://...` (PXE in-band
  delivery is the entire basis of the `ApplyConfigInsecure` debate).
- That a fresh Tailscale authkey is required per install
  (`review-security.md` §3) — absent from v0.2 docs entirely.
- That `~/.local/state/nostos/` is per-user XDG state (perms
  unspecified in `design.md` D5).

## 6. Terminology inconsistencies (citations)

- "install method" / "boot method" / "provisioner method" —
  `specs/provisioner/spec.md` Req 1 title vs body vs `proposal.md` /
  `design.md` D1.
- "tpi provider" vs "tpi provisioner" — same `proposal.md` Capabilities
  bullet uses both.
- "BMC" overloaded across Turing Pi (D3), iLO/iDRAC (D7), Proxmox host
  (D4 implication).
- "Bootstrap" overloaded: Talos etcd bootstrap vs nostos install
  lifecycle (`design.md` D2 uses both senses).
- "Run log" / "JSONL log" / "event log" interchangeable across
  `design.md` D5, `tasks.md` 1.5, `specs/provisioner/spec.md` Req 5.
- "Schematic ID" / "schematic" / "schematic_id" — three spellings
  across `design.md` D3, `tpi-provisioning/spec.md` Req 2, `tasks.md`
  3.2.
- "deadline" vs "timeout" — `design.md` D1 (`bootDeadline`),
  tpi-provisioning spec ("within 2s"), `tasks.md` 3.4 (mixed). Decide
  absolute deadlines vs relative timeouts.
- "rendered config" / "rendered machineconfig" / "machineconfig file"
  — all three in `design.md` D2.
- Arch value: `amd64`/`arm64` in `NodeView` (D1) but image filename
  hard-codes `arm64` (`metal-arm64.raw.xz`). Spec scenarios should
  make the mapping explicit.

## 7. Scope-drift indicators

- `tasks.md` §7 lists 12 deferred items; five already have v0.2 schema
  or interface surface (enum entries, JSONL log, BMCSemaphore, doctor
  stub, diff stub). Each pre-commits v0.3 work to v0.2 review.
- `proposal.md` "What This Is Not" is phrased as anti-features, not as
  v0.2-vs-brief carve-outs. "Not parallel installs (locks ship, flag
  does not)" is buried in `design.md` D4 rather than the non-goals.
- `00-brief.md` requested both a minimum slice and v1.0 sketches. The
  planner delivered both; the proposal claims only the first. The
  brief/proposal mismatch is itself scope drift.

## 8. Recommendations

1. Add a `glossary.md` defining: RK1, BMC, maintenance mode, ready,
   drift, idempotent (per phase), boot method, provisioner, run log,
   deadline vs timeout. Cite from each spec Requirement.
2. Replace `ready` with concrete sub-events (`apid-up`, `etcd-up`,
   `kubeconfig-fetched`, `install-complete`); pin scenarios to the
   strongest one observable per role.
3. Define `WaitMaintenance` success via `talosctl --insecure version`,
   not a TCP probe; update tpi happy-path scenario accordingly.
4. Restrict `boot.method` enum to `{pxe, tpi}` for v0.2; defer the
   other four to their own changes.
5. Tighten §4.2 / §4.3 signals to behavior over string match; prefer
   state assertions to event-sequence golden tests.
6. Promote §2 hidden constraints to explicit Preflight rules or
   non-goals.
7. Pick one Taskfile-shim strategy and one alias strategy.
8. Generalize lab-specific IPs in spec scenarios; document the test
   fixture once.

## 9. Bottom line

The change reads as a v0.2 implementation plan stapled to a v1.0
architecture sketch. The ambiguity that matters for implementers is
not in the architecture (over-specified) but in the operational
definitions (under-specified): what is "ready", what is "drift", what
"maintenance mode" means as an *observable*, what counts as a
successful tpi flash. Until those are pinned, spec scenarios check
strings, not behavior. Next move: glossary pass + acceptance-signal
audit, not more design.
