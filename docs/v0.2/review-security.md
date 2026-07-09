# Security Review: openspec/changes/nostos-v02-provisioners

Scope: trust boundaries, BMC creds, Tailscale authkey lifecycle, machine
token / CA exposure, first-boot insecure window, run-log redaction, image
cache integrity, subprocess invocation hygiene, concurrent-operator races,
1Password CLI session assumptions, backwards-compat traps.

Files reviewed: proposal.md, design.md, tasks.md,
specs/provisioner/spec.md, specs/tpi-provisioning/spec.md,
v0.2/brief.md, review-critic.md.

## TL;DR

Design D7 reads as an honest threat model and gets the obvious things
right (no inline creds, env over argv where the tool allows, redaction
at emit time, 0600 on rendered configs). The gaps are not in what is
named; they are in what is named but not enforced and what is enforced
in the wrong layer. Specifically: redaction is policy stated in prose
with no runtime hook; image integrity is "matches expected sha256" with
no documented source for "expected"; the Tailscale authkey is the secret
most likely to leak through this pipeline and is not mentioned at all;
the BMC LAN trust boundary is acknowledged then ignored when the spec
hard-codes a 50000/TCP liveness probe that doubles as a maintenance-
mode discovery pattern; and several backwards-compat carve-outs (the
`nostos up` alias, the unused provisioner enum entries, the absent
collision check on `host+slot`) each introduce a way to weaken the
security posture without changing a single secret reference.

## 1. Trust boundaries

D7 names operator laptop (trusted), BMC LAN (semi-trusted), maintenance-
mode Talos (insecure window). Missing:

- **Tailscale tailnet vs lab LAN.** The Tailscale extension on
  `talos/nodes/*.yaml` carries an authkey rendered into the
  machineconfig. The trust boundary "operator laptop -> rendered config
  -> tpi/iPXE delivery -> node" is not described per-hop.
- **factory.talos.dev** is an external trust dependency and should be
  named (image cache integrity, Section 7).
- **1Password cloud / `op` daemon** delegates trust; runtime memory of
  `op` is readable by any same-uid process. Not stated.
- **Run-log destination** (`~/.local/state/nostos/runs/`) lacks a
  documented threat model (Section 6).

Recommendation: add a "Subjects, Assets, Boundaries" table to D7 listing
BMC creds, Tailscale authkey, Talos machine CA, kubeconfig, rendered
machineconfigs, image cache, run logs against the boundaries they cross.

## 2. BMC credentials handling

Strengths: `_ref` suffix; inline values rejected (tasks 2.4); env over
argv for tpi (D3); redaction policy on emits; secrets banned from
JSONL (provisioner spec).

Real gaps:

- **Identity-file ref.** `boot.tpi.identity_file_ref` resolves to key
  material; tpi consumes a path. The spec bans the password value in
  argv but says nothing about the key-file path. Required: create with
  `O_CREAT|O_EXCL` mode 0600 inside a 0700 dir under `$XDG_RUNTIME_DIR`
  (or `~/.cache/nostos/secrets/<run-id>/`); stat after open to refuse
  symlinks; unlink in Cleanup even on Ctrl-C path.
- **`tpi --user` / `--password` argv fallback.** Some tpi versions
  ignore env. Spec must commit to "abort cleanly if env is not honored"
  rather than silently fall back. Pin a minimum tpi version in
  Preflight (review-critic 4.3).
- **Validator regex on `_ref`.** D7 lists `op:// | sops:// | env:// |
  file://`. `env://` exposes via `/proc/<pid>/environ` to same-uid; pure
  `file://` re-opens the at-rest question. Prohibit `env://` for BMC
  creds in v0.2; require `file://` to be 0600 + same-uid + no symlink.
- **Cred lifetime.** Resolved secrets travel through `Deps` into
  providers. Required: zero on Cleanup; never embed in returned errors
  (errors get logged by the CLI top-level).

## 3. Tailscale authkey lifecycle (NOT addressed)

Largest unmentioned secret. Risks specific to v0.2:

- Authkey lands in the rendered file at
  `nostos/state/configs/<name>.yaml` (0600), in the `-f <path>` argument
  to `talosctl apply-config -i`, and on disk for the install duration.
- Reinstall is destructive but the spec does not require rotating the
  authkey or removing the old machine from the tailnet. Re-using an
  authkey across hardware generations is a footgun; single-use keys
  silently fail on retry.
- A debug emit that prints rendered machineconfig contents would put
  the authkey into JSONL.
- **No revocation hook.** Failed install consumes one authkey use with
  no Cleanup-time revoke.

Required for v0.2:

- Spec statement: "Tailscale authkeys SHALL be ephemeral, single-use,
  TTL <= 1 hour; the operator runbook for `node install` rotates the
  `op://` reference value before invocation."
- Provider MUST NOT log rendered machineconfig contents at any level.
- `cluster.ApplyConfigInsecure` SHOULD pass the rendered file via stdin
  or via a temp path it removes after success/failure -- not leave it
  in `nostos/state/configs/<name>.yaml` indefinitely.
- Reference a future `nostos secrets rotate --tailscale` so this does
  not get forgotten in v0.4.

## 4. Machine token / CA exposure

The Talos cluster CA, machine token, and kubeconfig are produced by
`internal/secrets`. v0.2 adds new travel paths:

- `talosctl apply-config -i` now invoked for **every** provider (D2).
  Path on argv is fine; the file content is not. The 0600/0700
  perm-check in D7 must run on every install run, not only at first
  render.
- Machineconfig contains controlplane PKI bootstrap secrets for CP
  installs. There is no static check that the rendered config's role
  matches `nv.Role`. Add: orchestrator validates `machine.type` equals
  `nv.Role` before any provider sees the path.
- `FetchKubeconfig` writes `talos/kubeconfig`; perms inherited from
  v0.1 -- pin as a non-regression invariant in the spec.
- Under D2 unification the machineconfig is delivered to the node twice
  on the LAN in PXE mode: in the iPXE chain AND via `apply-config -i`
  (review-critic 3.2). Double-exposure on the LAN is bad even if the
  LAN is "trusted". Make ApplyConfigInsecure provider-optional.

## 5. First-boot insecure window on the LAN

D7 punts ("same as v0.1") via Q6. v0.2 widens it:

- `WaitMaintenance` polls `nv.IP:50000`. apid in maintenance mode listens
  on **all** interfaces; any host on the lab LAN can race nostos with
  its own `talosctl --insecure ... apply-config`.
- TCP-only probe is detectable from the LAN: passive observers learn
  *when* a node is in maintenance and can target it. Use an
  authenticated probe (`talosctl --insecure version`) and verify the
  peer presents the expected machine UUID / serial / hardware
  fingerprint before handoff.
- v0.3 parallelism multiplies open windows simultaneously. No mention of
  source-IP restriction. Document: BMC LAN must be isolated from
  untrusted clients during install windows.
- **Concurrent install on same IP.** Two `nostos node install tp1`
  invocations: one wins the apply-config race; the loser may apply over
  a node that already left maintenance mode, with non-deterministic
  outcome. Add lockfile (Section 9).

Add a spec scenario: "WaitMaintenance verifies node identity before
ApplyConfigInsecure."

## 6. Run-log redaction soundness

Stated controls (D7 + tasks 1.6): providers emit pre-redacted strings;
a static lint test scans corpora for known secret-shaped substrings.

Soundness: **fragile, not enforced at runtime.**

- Static lint catches synthetic test corpora only. Production secret
  values (a 40-char Tailscale key, a base64 BMC password) never appear
  in fixtures and never trip the lint.
- Defense in depth requires a runtime Scrubber wrapping the
  EventEmitter, seeded with resolved-secret values for the run. D7
  hand-waves `redact.Strings()` but the interface in D1 does not show a
  wrapping point and D2 tees emit through runlog before any redaction
  layer is named.
- The `phase` field that the spec scenario requires in JSONL is missing
  from the EventEmitter signature (review-critic 2.1). The scrubber
  must process all fields, not just `message`.
- **tpi stdout streaming** (tasks 3.8) at 200ms throttle is the single
  biggest leak vector. Required: scrub each chunk against the current-
  run secrets table before emit; do not emit raw lines.
- **Cred-rotation aftershock.** Old logs that contained a then-secret
  value remain unredacted forever. Document: treat `runs/` as
  sensitive; clean on cred rotation. Add `nostos run gc` to v0.2 (not
  v0.4).
- File mode unspecified. Required: 0600 on each JSONL, 0700 on parent,
  same-uid; verify at open.

## 7. Image cache integrity

Threat: a poisoned `metal-arm64.raw.xz` written to
`~/.cache/nostos/images/...` is flashed unverified to RK1 eMMC and runs
as root. Cache compromise compromises every node installed from it.

Spec text says "matches the expected size and sha256." It does not
specify where the **expected** sha256 comes from on first download.

Required for v0.2:

- Source of truth must be one of: (a) pinned in `nostos/config.yaml`
  under `cluster.image_digests` (operator-controlled, git-auditable);
  (b) fetched over HTTPS from a documented factory.talos.dev manifest
  with a documented cert pin. TOFU is acceptable only if the design
  explicitly says so and the first download is treated as a trust-
  establishment moment.
- Cache root 0700, files 0600. No execve from the cache.
- Decompression: use a Go xz library (review-critic 4.2), not a shell
  out, both for portability and to avoid passing attacker-controlled
  filenames to a child process.
- Cross-cluster sharing on the same workstation: schematic_id MUST be
  in the path (D3 already does) AND a GC story is needed so old
  schematics cannot be silently re-used.

## 8. Subprocess invocation: env vs argv vs stdin

| Tool | Secret | Current spec | Verdict |
|------|--------|--------------|---------|
| `tpi` | BMC user/pass | env (`TPI_USERNAME`/`TPI_PASSWORD`) | OK with version pin |
| `tpi` | identity_file path | argv path | OK iff temp file 0600 + auto-unlinked |
| `op` | session token | inherited env | See Section 10 |
| `talosctl` | machineconfig file | argv `-f <path>` | Path OK; file perms 0600 |
| `talosctl` | endpoint, ca | env / talosconfig | OK; document path |
| `xz` (if shelled) | filename | argv | Avoid shell out; use Go lib |

Concrete asks:

- Use `(*exec.Cmd).Env` exclusively; never construct env via
  `"KEY=" + value` concatenation in argv.
- Never invoke a shell. All tools called as exec, argv-form, no
  string interpolation.
- If a future tool forces argv-only secrets, use stdin: write to the
  child's stdin pipe.
- Capture child stdout/stderr through the Scrubber. Do not print raw
  child output to the terminal.
- Scrub child env before exec: do not pass `OP_SESSION_*` to children
  that do not need it.
- Unit test asserts argv contains no occurrence of the resolved
  password value AND env contains it. Both halves needed.

## 9. Concurrent-operator races

Proposal "Non-Goals" excludes multi-operator; spec is silent on the
symptom. v0.2 ships BMC contention only within one process.

- Two laptops both run `nostos node install tp1`. In-process BMC mutex
  does not protect; two `tpi flash` calls hit the BMC. Some BMCs
  serialize; some return ambiguous errors mid-flash and brick the
  module.
- Two `nostos` processes on one workstation collide on
  `~/.cache/nostos/`. Required: write to sibling temp file, fsync,
  rename atomically.
- Run-id collision risk: time+pid IDs collide within a ms. Use ULID /
  UUIDv4.
- `nostos/state/configs/<name>.yaml` shared between concurrent installs
  of the same node. Last writer wins; the loser may apply a stale
  config rendered before an `op://` value rotated. Required: per-node
  `flock` on `nostos/state/configs/<name>.lock` held across Render ->
  ApplyConfigInsecure.
- ApplyConfigInsecure window race (Section 5) is the cross-laptop
  variant of the same lock failure.

Add a v0.2 spec scenario: "Concurrent install of the same node aborts
fast with a clear lockfile error."

## 10. 1Password CLI session assumptions

D7: "Operator laptop is trusted (already runs `op signin`)." Reality is
more nuanced.

- `op signin` produces a short-lived session token in env. The session
  can expire mid-install. There is no spec for what happens if `op
  inject` returns "session expired" mid-run. Required: every secret
  resolution goes through one helper that detects expiry and aborts the
  run cleanly (no half-applied state).
- The desktop daemon path does not set `OP_SESSION_*`; `op` reads its
  own socket. Provider must trust `op`'s exit code, not env presence.
- **Session token in child env.** Strip `OP_SESSION_*` from `tpi` /
  `talosctl` exec env.
- Resolve each secret once per run, hold in memory, zero on exit. Avoid
  rate-limit spirals from re-resolution.
- `op://` ACL changes between runs: spec must say secrets are resolved
  at Preflight and not re-resolved later.
- The repo's `talos/op/` directory is gitignored per CLAUDE.md; nostos
  v0.2 must verify its equivalent (`nostos/state/configs/`) is
  gitignored AND check at startup that no rendered file is tracked.

## 11. Backwards-compat traps that weaken security

- **`boot.method` defaults to `pxe` on absent block** (D9; tasks 2.1).
  A node entry losing its `boot:` block silently becomes PXE; nostos
  stands up a PXE server on the LAN and waits. Mitigation: warn loudly
  on default; require explicit `method` for any node added after v0.2.
- **`nostos up` alias kept for one release.** Spec must require the
  alias is a thin shim that runs the SAME Install code (same
  Preflight, same redaction, same lockfile). A separate code path is
  a security regression vector. tasks 5.2 implies a shim but does not
  pin behaviour.
- **Six-method enum, two implementations** (review-critic 1.1). Tasks
  2.3 places vm-pc01 with `boot.method=proxmox` while no proxmox
  provider exists. Either the validator silently accepts unsupported
  methods (footgun) or rejects (then 2.3 fails). v0.2 should fail
  closed: validator accepts only implemented methods.
- **Old Taskfile entries kept as wrappers.** The old recipes do NOT go
  through the secrets pipeline. Replace bodies with `echo "deprecated;
  use task nostos:install" && exit 1`, not a wrapper that still works.
- **`(host, slot)` collision not validated** (review-critic 4.9). Two
  nodes pointing at the same slot is destructive AND a credential-
  confusion vector. Validator must enforce uniqueness.
- **Reinstall has no confirm** (review-critic 4.10/4.11). Typo in cron
  / Taskfile wipes a healthy worker. Spec MUST require interactive
  confirmation OR `--yes` with a 5-second cancellable banner; MUST
  refuse to flash a node currently presenting a live, healthy Talos
  identity unless `--reinstall` is explicit.

## 12. Spec/design fixes that are security-blocking

1. EventEmitter (D1) needs a `phase` field AND must route through one
   Scrubber owned by the orchestrator. Provider discretion is unsafe.
2. ApplyConfigInsecure unification (D2) must be re-spec'd so PXE does
   not re-apply config out of band; otherwise the secret travel path
   doubles for no reason (Section 4).
3. Maintenance-mode probe must verify peer identity (Section 5).
4. tpi version pin and key-file lifecycle must be specified
   (Sections 2, 8).
5. Image sha256 source of truth must be documented (Section 7).
6. Tailscale authkey treatment must be added to D7 (Section 3).
7. Run-log redaction must be implemented at the emitter sink, not by
   provider convention; static lint stays as defense in depth only
   (Section 6).
8. Concurrent-operator: per-node `flock` and explicit failure spec
   (Section 9).
9. Alias and enum carve-outs must fail closed (Section 11).

## 13. Spec scenarios to add

- "Tailscale authkey is single-use and rotated per install run."
- "Rendered machineconfig file is unlinked after ApplyConfigInsecure."
- "Concurrent install of the same node aborts with a lockfile error."
- "WaitMaintenance verifies the peer's machine identity."
- "Cached image SHA-256 matches a digest from `cluster.image_digests`."
- "Cleanup unlinks any temp credential materialization (key files,
  stdin pipes) even on Ctrl-C."
- "`boot.method` default is logged at WARN; new nodes require explicit
  method."
- "Run-log JSONL is mode 0600 in a 0700 directory; nostos refuses to
  start an install if the directory is world-readable."

## 14. Five questions for follow-up

1. Where does the expected sha256 for cached factory.talos.dev images
   live, and what is the trust chain on first download?
2. Is there a single Scrubber sink wrapping every emit and every child
   stdout/stderr, seeded with the resolved-secret table for the run?
3. What is the spec for the Tailscale authkey: ephemeral, single-use,
   TTL-bounded, rotated per install? Where is rotation implemented or
   deferred?
4. How is ApplyConfigInsecure reconciled with PXE in-band delivery so
   the machineconfig is not exposed twice on the LAN?
5. What prevents a second concurrent `nostos node install <name>`
   (same or different operator) from racing the first into apply-config?
