# Hard-Nosed Critique: openspec/changes/nostos-v02-provisioners

Files reviewed:
- proposal.md
- design.md
- tasks.md
- specs/provisioner/spec.md
- specs/tpi-provisioning/spec.md
- .openspec.yaml

Reference: brief at v0.2/brief.md.

## TL;DR

The change is competently structured and reads like the author has actually
operated the lab. But it commits the classic v0.x sin: it tries to lock in
the shape of v0.3, v0.4, and v1.0 *while* claiming to ship a smallest slice
that closes the most painful gap. The Provisioner interface, the run-log
JSONL, the inventory schema, the doctor catalog, the BMC contention model,
and a six-method enum are all baked in now with little operational evidence
behind them. The actual v0.2 deliverable -- get tp1 and tp4 reinstalled via
nostos node install -- is buried under a meta-architecture written for a
fleet that does not exist in this lab.

The single most damaging design call is D2 every provider funnels through
ApplyConfigInsecure (design.md, D2). It is symmetry-for-its-own-sake. PXE
in v0.1 delivers the rendered Talos config in-band via the iPXE chain
(talos.config=http://...); calling talosctl apply-config -i a second time
after the node has already self-applied is at best a no-op race against
maintenance mode closing, at worst a double-apply that fails or quietly
mutates dell01. Either way, the proposal claim no external behavior change
for dell01 (proposal.md, Modified Capabilities, pxe-provisioning) is in
direct tension with the orchestrator reshape unless the PXE provider
short-circuits ApplyConfigInsecure -- which means the orchestrator is no
longer provisioner-agnostic in any honest sense. Pick one and own it.

## 1. Scope and over-engineering for v0.2

### 1.1 Six methods in the enum, two implemented
config schema v2 (tasks.md 2.1) hard-codes the enum
pxe|tpi|redfish|proxmox|usb|rpi-imager. Four of those have no provider in
this change. Burning the enum names into the validator now means:
  - tasks.md 2.3 explicitly adds vm-pc01 with boot.method=proxmox -- a
    method whose provider does not exist in v0.2. Either validation will
    reject it (contradiction with 2.3 acceptance: nostos node list prints
    all four nodes) or the validator silently accepts unimplemented
    methods, which means the enum guard 2.1 promises is theatre.
  - The forward-compat shape of redfish/proxmox/usb/rpi-imager configs is
    being decided NOW with no implementation pressure. RedfishBoot,
    ProxmoxBoot, USBBoot, RPiBoot pointers (design.md D1) are reserved
    bytes in the public schema. When v0.3 actually writes redfish, the
    real shape will differ and migration will be on the operator.

Recommendation: ship v0.2 with method enum {pxe, tpi} only. Add v0.3
methods when v0.3 lands. The interface is enough decoupling.

### 1.2 Run-log JSONL exists for a future feature
D5 (design.md) admits openly: v0.2 deliverable: write the log and tail it.
v0.3 deliverable: --resume reads the log. Writing a structured log purely
so a future version can read it is the textbook definition of speculative
generality. None of v0.2 needs the JSONL; the orchestrator already emits
events to a channel and the operator can tee stderr.

Worse, the redaction story (D7 + tasks 1.6) hangs on a static lint test
that scans for known secret-shaped substrings in test corpora. That does
nothing at runtime. If a future provider emits a credential into Event,
the JSONL will leak it; the build-time lint will not catch it because the
build-time corpus is synthetic. The spec explicitly demands secrets never
reach the run log (specs/provisioner/spec.md, Scenario: Secrets never
reach the run log) but the design provides no runtime mechanism to
enforce the requirement. This is a gap, not a control.

If you keep the run log, redact at the EventEmitter sink, not at provider
discretion. emitter.Wrap(redact.NewScrubber(resolvedSecrets)) at the
orchestrator. Then every provider gets it for free and the spec scenario
is actually satisfiable.

### 1.3 Inventory.db reserved in design.md D6
SQLite schema is committed in v0.2 design even though the table is not
created until v0.3. A future implementer is welcome to redesign it; why
freeze a column list now? Move D6 to the v0.3 change document where it
will get scrutiny against actual usage.

### 1.4 Doctor catalog (D8) and roadmap claims
D8 enumerates checks that v0.4 will ship. tasks.md 5.5 ships a stub doctor
that exits 2. Both are noise in v0.2. Drop or move to a roadmap.md.

## 2. Logical gaps in the Provisioner interface (design.md D1)

### 2.1 EventEmitter signature does not match D5
D1 declares:
  type EventEmitter func(kind, message string)

D5 says runlog lines include phase. There is no phase argument on the
emitter. Either every provider has to encode phase into kind or message
(brittle), or the orchestrator has to wrap the emitter per-phase
(unspecified). The spec scenario (specs/provisioner/spec.md, Run log
captures install events) requires phase in the JSONL. With the current
signature, you cannot satisfy that scenario without changing the
interface -- which means the v0.2 interface is wrong on day one.

Fix: emitter is (phase, kind, message string) or accepts a struct. Decide
now.

### 2.2 WaitMaintenance signature drift
D1 lists WaitMaintenance(ctx, n, emit). D2s reshape pseudocode has
WaitMaintenance(ctx, nv, opts.bootDeadline(), emit). Three vs four args.
Either the deadline is on the context (then opts.bootDeadline is
redundant) or the deadline is an explicit argument (then D1 is wrong).
specs/provisioner/spec.md implies a context with a deadline. Pick one.

### 2.3 Cleanup with non-cancelled context
specs/provisioner/spec.md explicitly requires Cleanup runs after Ctrl-C
with a non-cancelled context. D2s defer prov.Cleanup(context.Background(),
nv, emit) implements this -- but it conflicts with D4: the BMC mutex was
acquired with the run context. If Cleanup needs the BMC (e.g. tpi power
off on failure), and the lock has been released by the deferred return,
two concurrent installs could collide on the same BMC during cleanup.
The spec says nothing about lock ordering with respect to Cleanup. This
is a real concurrency hole, not a hypothetical.

Also: Cleanup default 30s timeout (specs/provisioner/spec.md). tpi power
off can take longer than 30s on a flaky BMC. No retry, no backoff
specified. A timed-out Cleanup leaves the slot in an indeterminate power
state, which is the exact failure mode tpi-provisioning/Cleanup powers
the slot off after a failed flash claims to prevent.

### 2.4 NodeView claims to decouple, actually couples
D1 NodeView and ViewFrom (tasks 1.3) are an anti-pattern. The intent
is "providers do not import internal/config". The result is that every
field added to Node must be added to NodeView too, with a translation
layer in ViewFrom. That is the same coupling, just spread across two
packages. If you really want decoupling, give the provider a typed
*BootConfig and let it take what it needs. NodeView duplicates Node 1:1
in practice; review specs/provisioner/spec.md, NodeView is the only data
passed to providers -- it lists name, MAC, IP, role, arch, install_disk,
template, BootConfig. That IS the Node. Drop NodeView.

### 2.5 Five lifecycle hooks vs five real phases
Preflight, Prepare, Boot, WaitMaintenance, Cleanup. This is fine, but the
spec for tpi (specs/tpi-provisioning/spec.md) makes Prepare do image
download AND decompression while Preflight checks 4 GiB free. What if
the image is 8 GiB? Preflight passes (the cache is 4 GiB free, sized for
the compressed file), Prepare runs out of disk during decompression. The
specs 4 GiB threshold (spec.md) is a magic number with no derivation.

Worse, the 4 GiB threshold hard-codes the metal-arm64.raw.xz era. When
schematic IDs gain extensions (NVIDIA, qemu-guest-agent, etc.), the image
grows. Preflight must compute required-bytes(version, schematic) and
compare. Spec hard-codes 4 GiB.

## 3. Orchestrator reshape problems (design.md D2)

### 3.1 Render before Preflight breaks "no side effects before preflight"
D2 sequence: Render machineconfig -> Preflight -> Prepare -> Boot. But
Render writes to nostos/state/configs/<name>.yaml on disk. That is a
side effect. Preflight is supposed to be cheap, idempotent, no-side-
effect. If Preflight fails after a Render, you have stale rendered config
on disk that may include freshly resolved op:// secrets at 0600. The
right order is Preflight -> Render -> Prepare -> Boot, with Render owned
by the orchestrator (not the provider).

### 3.2 ApplyConfigInsecure unification (the big one)
Already called out in TL;DR. Restating: PXE-via-iPXE delivers the rendered
config in the boot.ipxe chain. Adding talosctl apply-config -i AFTER iPXE
boot is double-apply. Talos in maintenance mode allows --insecure config
once; once a config is applied the node leaves maintenance mode and the
insecure listener closes. The race window is small but real.

The spec scenario Existing PXE flow goes through the interface
(specs/provisioner/spec.md) lists ApplyConfigInsecure as one of the
ordered steps for dell01. That is wrong on day one and will fail the
golden test (tasks 4.1) it is meant to validate.

Fix: ApplyConfigInsecure becomes provider-optional. The provider declares
whether config delivery is in-band (PXE) or out-of-band (tpi, redfish,
proxmox, usb). Or, better: the orchestrator calls
provider.DeliverConfig(rendered) and PXEs implementation is "wait for the
node to fetch /configs/<mac>.yaml" while tpis is the apply-config -i.
Then the lifecycle has six honest phases, not five plus an awkward
appendix.

### 3.3 PXE goroutine ownership
D3 PXE refactor: HTTP-request tap becomes a goroutine inside the provider
that emits download events. Provider Boot returns when the boot signal
has been sent, NOT when the node is up (D1). Then the orchestrator
proceeds to WaitMaintenance, while the provider goroutine is still
alive emitting events. Who owns the goroutine context? Who joins on it?
What happens when WaitMaintenance returns success but the goroutine is
mid-emit? Spec is silent. This is the kind of detail that bit the v0.1
PXE flow (orchestrate.go:122-160) and the refactor moves it without
explaining how it improves.

### 3.4 BMCKey + separate "pxe:server" lock
D4: PXE provider returns BMCKey()="" but the orchestrator gates PXE Boot
on a separate keyed lock pxe:server. The orchestrator now has two
distinct contention models: (a) BMCKey from the provider, (b)
hardcoded knowledge of PXE specifics. That is the exact coupling D1
claims to remove. If a future redfish provider has a similar single-
threaded resource (e.g. one HTTP server for virtual-media artefacts), the
orchestrator will need a third lock or a more general API. Either:
  - BMCKey returns ANY contention key (renaming required), or
  - Provider exposes Resources() []string and the orchestrator gates on
    each.

The current spec (specs/provisioner/spec.md, BMC contention key) commits
to BMCKey but the actual implementation per D4 needs more. Spec lies to
the implementer.

## 4. tpi provider gaps (specs/tpi-provisioning/spec.md, design.md D3)

### 4.1 Image SHA-256 source unspecified
spec.md "matches the upstream factory.talos.dev sha256". factory.talos.dev
does not (as of writing) publish per-image SHA-256 alongside downloads in
a stable JSON manifest. design.md D3 hand-waves cached file matches the
expected size and sha256. Where does "expected" come from? Re-downloading
to compare is circular.

Either:
  - Pin the SHA in nostos/config.yaml under cluster.talos_version (now the
    operator owns it; ugly but explicit).
  - Compute the SHA after first download and persist beside the cached
    file (last_downloaded_sha); skip-if-match is then defensive against
    partial writes only, not against compromise.

The spec "no HTTP GET to factory.talos.dev" (Scenario: Second install
reuses the cache) presumes a trusted prior. Document it.

### 4.2 Decompression dependency
design.md D3: xz -d to a sibling .raw file (sparse-aware). tasks 3.3:
xz Go module or shelled-out xz -d. Shelling out reintroduces a runtime
dep right when v1.0 vision (proposal: vendored iPXE, kill Docker) wants
to delete runtime deps. Pick the Go xz library (github.com/ulikunitz/xz)
and stop debating.

### 4.3 tpi binary version compatibility
Preflight checks tpi --version succeeds (specs/tpi-provisioning/spec.md).
That confirms the binary is on PATH. It does NOT confirm the version is
compatible with the argv shape this code uses. The tpi CLI has had
breaking syntax changes (e.g. -n vs --node; --host vs URL). Pin a minimum
version in Preflight: parse the version and reject below known-good.

### 4.4 BMC firmware compatibility
No mention of Turing Pi BMC firmware version. tpi flash semantics depend
on BMC build. Provisioner should query BMC version (the API exists) and
warn or fail on unknown. tasks.md says nothing.

### 4.5 Power-state assumptions
design.md D3 Boot phase: tpi --host <h> power off -n <slot> first. Good.
But if the slot was already off (operator just came in from cold start),
tpi power off may return non-zero. Spec does not say whether non-zero
from power off is fatal. It probably should be ignored, but the design
needs to commit.

### 4.6 Identity-file ref delivery
design.md D3 boot.tpi.identity_file_ref pulls from op://. Then what?
tpi expects a file path on disk. The provider must materialize the secret
into a temp file at 0600, pass the path to tpi, and unlink afterward.
None of this is specified. The spec scenario "Resolved password not
present in argv" (specs/tpi-provisioning/spec.md) covers passwords but
not key files. A key file path will be in argv by definition. The threat
model (D7) does not separate "key material" from "key path".

### 4.7 WaitMaintenance is wrong
design.md D3 + tasks 3.6: poll TCP nv.IP:50000 every 5s. TCP listen does
not mean apid-is-healthy-in-maintenance-mode. A node can listen on 50000
during early boot before maintenance mode is ready. The right probe is
talosctl --insecure -n <ip> version (or apid GetVersion). The spec
scenario (Happy-path install) says waits for apid at 192.168.68.107:50000
which is the same wrong abstraction.

### 4.8 Default deadlines vs reality
design.md D3 Default 10 min. Real-world tpi flash on RK1 eMMC is typically
4-7 minutes; flash + power cycle + Talos boot to maintenance mode can
exceed 10 min on a cold RK1. Default 10 min will time out happy paths
and the operator will think it failed. Defaults: 20 min boot deadline,
or compute from arch/disk size.

### 4.9 Slot/host validation
design.md D3 tpi.slot in 1..4. tasks 2.2 validates Host + Slot. Nothing
checks that two nodes do not share (host, slot). tasks 2.2 says
"Validation rules" -- spec needs an explicit collision rule:
"For all nodes with boot.method=tpi, (boot.tpi.host, boot.tpi.slot) must
be unique." Otherwise the operator can footgun by pointing tp1 and tp4
at the same slot, and a flash will overwrite the wrong module.

### 4.10 Reinstall semantics undefined
PXE has an explicit wipe queue (state/pending-wipes.json from v0.1). tpi
flash is destructive every run -- the eMMC is overwritten unconditionally.
What about the operators expectation that reinstall confirms first?
proposal.md says "Run task nostos:install NODE=tp1. Operator confirms the
destructive flash via interactive prompt (or --yes for scripted use)"
(D9 Migration steps) but this confirmation is not in any task or scenario.
Spec it.

### 4.11 Already-running node
Missing scenario: tp1 is healthy and joined to the cluster; operator
accidentally runs nostos node install tp1. Today there is nothing
preventing a destructive reflash of a working node. D9 hints at "y/n
prompt" but no spec. v0.1 had wipe-on-purpose semantics; v0.2 tpi
provisioner has wipe-by-default with no guard.

## 5. Security model (design.md D7) is partially fictional

### 5.1 Credential-shaped value detector
tasks 2.4: "Validator MUST fail on any field whose name does not end in
_ref but contains a credential-shaped value." There is no such thing as
credential-shaped at the YAML layer. A 24-char string is not a password
to a regex; entropy heuristics produce false positives on hostnames and
false negatives on dictionary passwords. This guard will be either
useless or annoying. Replace with: schema explicitly types credential
fields as Ref types (custom YAML unmarshaller that REQUIRES a URI prefix
op://, sops://, env://, file://); any non-URI string fails to unmarshal.
Then the validator is the schema, which is correct.

### 5.2 First-boot insecure window deferred
D7: maintenance-mode Talos is insecure by design. Q6: "recommend a
temporary firewall rule on the operator laptop side, or accept this is
the same window v0.1 already has?" The proposal accepts. Fine for v0.1
where one node was provisioned at home over a trusted LAN. v0.2 adds
multi-node via tpi over the BMC LAN, which the design itself flags as
"semi-trusted at best." The exposure window grows with parallel installs
(v0.3+). At minimum, the spec should require maintenance-mode probes be
restricted to nv.IP and not broadcast. There is no mention.

### 5.3 Run log retention
D5 punts rotation policy to v0.4. JSONL files in
~/.local/state/nostos/runs/ accumulate forever. Each contains node
metadata, schematic IDs, sometimes stack traces. Over a year of
operation that is not big bytes-wise but it is a privacy/forensics
liability with no GC story. Add  to v0.2,
not v0.4. Cheap to implement, real to operate.

## 6. Inconsistencies between docs

### 6.1 tasks 2.3 contradicts proposal "What This Is Not"
tasks 2.3 puts vm-pc01 in nostos/config.yaml with boot.method=proxmox.
proposal "Replaced flows" / Migration: "vm-pc01 (Proxmox VM) install
deferred to v0.3". If the method is invalid in v0.2, listing the node
breaks "nostos node list prints all four nodes". If the method is valid
(silently tolerated), the schema enum is meaningless. Either gate on
implemented-providers-only or commit to lazy validation.

### 6.2 tasks 5.3 vs spec scenarios
tasks 5.3: --parallel hidden flag, returns "parallel installs land in
v0.3". specs/provisioner/spec.md scenario "Two slots on one Turing Pi
serialize" exercises --parallel 2. The scenario will fail if the flag is
hidden/disabled. Fix: either ship --parallel real (and own the
concurrency tests), or drop the scenario from v0.2 spec and re-add in
v0.3.

### 6.3 v0.3/v0.4 sketches in design vs proposals "Non-Goals"
proposal "What This Is Not" excludes SaaS and multi-operator. design
"D6 Inventory Schema" admits one operator. Fine. But the proposal also
says "Not parallel-everywhere" while design ships the locks. Confusing
to a reviewer; consolidate.

### 6.4 .openspec.yaml is anemic
Just two fields: schema and created. nostos-v01/.openspec.yaml may have
more (id, title, status, capabilities). Verify against the v01 file
before merging; if missing fields, the openspec tooling probably wont
recognize the change.

## 7. Missed scenarios

These are the scenarios I would expect from a hard review and do not see:

- Network partition mid-flash. tpi flash succeeds; WaitMaintenance times
  out because the node IP DHCPed differently or DNS changed. Cleanup
  power-offs. Operator retries -- now the flash happens AGAIN even though
  the previous one succeeded. No idempotency on "did this slot already
  get our image?".
- BMC credential rotation. op:// reference value changes between two
  installs. Run log redaction was per-run; old logs may now contain a
  sub-string that is no longer a secret -- not a leak, but the redaction
  static lint test will yell.
- Concurrent operator. Two laptops both running nostos node install tp1.
  Out of scope per non-goals, but spec should state the symptom (last
  writer wins; second tpi flash interrupts the first leading to bricked
  module).
- talosctl version mismatch. Operator laptop has talosctl 1.9, cluster on
  1.10. ApplyConfigInsecure for any provider may fail silently. v0.1
  presumably had the same hole, but v0.2 generalizes the path.
- Maintenance mode never returns (e.g. wrong arch image flashed). Spec
  needs a recovery story: "tpi power off -n <slot>; redo with correct
  schematic". Right now the user gets ErrTimeout and no remediation.
- Node IP change. tp1 IP is hardcoded (192.168.68.107). What if the
  router hands out a different lease? The spec assumes static reservation
  but never demands it.

## 8. Roadmap bets I would not take

- v0.4 splits nostos into nostos-pxe and nostos-bmc daemons with gRPC.
  For a home lab with 4 nodes, this is cargo-culted from cloud
  infrastructure. A daemon has lifecycle, logs, metrics, and config of
  its own; you have just multiplied the operator burden by 3. Drop until
  there is a real reason (e.g. "I want to PXE-boot from a Pi sitting next
  to the rack while my laptop is closed").
- v1.0 vendored iPXE binaries. Real value (kill the Docker dep), but the
  build system to produce them across BIOS+EFI+arm64 is non-trivial.
  Estimate the effort honestly before committing v1.0.
- v0.3 drift detection. Rendered config vs live machineconfig will diff
  on every comparison because templates render with op:// resolved to
  current secret values, while live machineconfig has whatever was
  applied at install time. False positives are guaranteed unless drift
  detection ignores resolved-secret subtrees -- which means writing a
  YAML-aware diff that knows where secrets live. Non-trivial. v0.3 will
  not deliver this in a useful form.
- v0.4 secrets rotate. Rotating a Talos cluster secret means rewriting
  every machineconfig and re-applying via talosctl edit mc. Lumping it
  with cluster upgrade --to is two products. Split.
- "rpi-imager provider" for v0.4. The Talos rpi-imager flow is undocumented
  for headless use and brittle. Either operator-driven usb covers it
  (likely) or this provider is a permanent TODO.

## 9. Smaller things, no order

- proposal.md: "consumer config: nostos/config.yaml gains optional boot:
  blocks for tp1, tp4, vm-pc01 (added under section 2 of tasks.md).
  dell01 entry stays as-is." dell01 entry has no boot block today; the
  default is method=pxe. Fine, but write it down.
- design.md D9 "nostos up keeps working as alias for one release" --
  one release is undefined. After v0.3? After 90 days? Pin a date.
- tasks 1.2 "Register panics on duplicates" -- tested via unit. A panic
  in init() is a compile-time-equivalent failure; the binary refuses to
  start. Document this as user-visible.
- tasks 3.8 "200ms throttle to avoid event flood" -- 200ms is a magic
  number; pick on what evidence?
- design.md D10 "golden test ... pinned via testdata/golden/dell01-
  install.events.json". Golden test for an event sequence will be
  fragile against any phase-ordering change. Prefer asserting a
  topologically-ordered set of phases, not a literal sequence.
- specs/tpi-provisioning/spec.md happy-path scenario hard-codes
  192.168.68.10 as BMC and 192.168.68.107 as node IP. Specs should not
  be lab-specific. Generalize.
- proposal "tpi CLI (already on operator laptop per taskfiles/turing.yml)"
  -- what if the operator workstation is fresh? Bootstrap doc gap.
- tasks 5.2 "stderr includes the word deprecated" -- cute, but pin a
  format. Otherwise downstream tooling that greps will break across
  versions.

## 10. What I would actually ship in v0.2

A much smaller change:

1. Provisioner interface with three hooks (Plan, Apply, Cleanup) where
   Plan = combined Preflight+Prepare and Apply = combined Boot+
   WaitMaintenance. Three hooks is enough for two providers; expand when
   the third provider proves it.
2. pxe and tpi providers. No proxmox/redfish/usb/rpi-imager enum entries.
3. cluster.Install kept honest: the orchestrator calls the provider and
   knows nothing about how config gets to the node; provider owns
   delivery (in-band for PXE, out-of-band for tpi).
4. No JSONL run log. No inventory. No doctor stub. No --parallel hidden
   flag. Add when needed.
5. Clear migration: nostos node install replaces nostos up. Drop the
   alias at v0.3; do not promise "one release" of compat.
6. Spec scenarios cover ONLY pxe+tpi, with explicit destructive-reflash
   confirmation and (host, slot) uniqueness.

That ships in a week. The current proposal is two months of work, half
of it speculative. The brief asked for "the smallest slice that closes
the most painful gap" (proposal.md, paragraph one). The smallest slice
is much smaller than what is on the table.

## 11. Five questions for the security and architecture reviewers

1. What is the actual write-fence between iPXE-delivered config (PXE) and
   talosctl apply-config -i (tpi/redfish/proxmox), and does
   ApplyConfigInsecure as a uniform step actually hold without breaking
   PXE?
2. How is "credential-shaped value" defined such that the validator
   (tasks 2.4) is neither annoying nor evadable? Or should the schema
   type credential fields explicitly?
3. Where does the SHA-256 for cached factory.talos.dev images come from,
   and what is the trust chain on first download?
4. Cleanup runs with a fresh context (specs/provisioner/spec.md). How
   does it interact with the BMCSemaphore lock that was acquired with the
   run context, particularly under parallel installs?
5. tpi flash is destructive every run. What is the v0.2 spec for refusing
   to reflash a node that is currently a healthy cluster member, and
   does --yes / --force express the right failure mode?
