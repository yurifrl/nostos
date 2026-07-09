# Security Review — nostos v0.3 (dashboard + hygiene)

Scope: `openspec/changes/nostos-v03-dashboard-and-hygiene/`
(proposal.md, design.md, tasks.md, specs/dashboard/spec.md,
specs/cli-machine-output/spec.md). Cross-referenced against the v0.2
security review (`review-security.md`) and the v0.3 critic
(`v03-review-critic.md`). Focus: net-new attack surface introduced
by v0.3 — TUI, OAuth-driven mutation, BMC pre-flight, input
contract, public guide.

## TL;DR

v0.2 open gaps (authkey lifecycle, Scrubber sink, ApplyConfigInsecure
double-exposure, peer identity) are **not addressed** in v0.3 and
v0.3 makes several worse. Top concerns:

1. The Bubble Tea dashboard is a long-running process holding OAuth
   tokens, kubeconfig, BMC creds, with the right to mutate on one
   keystroke. No idle lock, no re-auth, no audit
   (`design.md:D8`/`D10`, `specs/dashboard/spec.md` Req 4).
2. `cluster cleanup --apply` does irreversible `DELETE
   /api/v2/device/<id>` gated only on `connected==false &&
   age_days >= 7` (`design.md:D11`, `tasks.md:2.4`) —
   clock-skewable and adversary-influenceable.
3. Input hardening (`tasks.md:3.7`) is a four-bullet list with no
   canonical validator package and no fuzz of `op://`.
4. TOFU fix (`design.md:D2`) is safer than v0.2 but recovery-branch
   + missing `fsync` + 24h `*.part` GC (critic §4.10) leave holes.

---

## 1. Stream A2 — TOFU race remediation

`design.md:D2` (l.95–145), `tasks.md:1.4`.

**Verdict: rename ordering is correct; recovery branch and `*.part`
GC are not.**

- Stream-hash + record-after-rename closes the v0.2 hole. ✓
- **Recovery branch** (`design.md:l.130-134`) — crash after rename,
  before digest write → next run rehashes a freshly-renamed file
  and treats the result as canonical. That IS TOFU on the recovered
  partial. If `cluster.image_digests` is unpinned (TOFU build-tag
  mode, l.144), a MITM that survived the rename gets promoted.
  Required: recovery branch MUST verify against
  `cluster.image_digests` or re-download — never fall through to TOFU.
- **No `fsync` specified.** Add `fsync(*.part)` before close,
  `fsync` parent dir after rename.
- **24h `*.part` GC unsafe** (critic §4.10): peer Ensure deletes
  in-flight downloads on slow links. Required: `flock` on the
  `.part`; GC refuses flocked files.
- **Build tag for TOFU is hand-waved** (l.144). Pin `dev_tofu`
  with a CI assertion release builds never carry it.
- **Hash collisions on recovered partial** — sha256 makes collision
  a non-threat cryptographically. The actual risk is "untrusted
  bytes promoted to canonical"; same fix as recovery branch.
- **Re-hash on every cache hit** (admitted 2s tax) defends a
  threat (third party rewriting a 0700 cache) the posture
  excludes. Drop, or drop same-uid posture.

## 2. Stream A3 — BMC pre-flight LAN disclosure

`design.md:D3` (l.147–195), `tasks.md:1.5–1.7`.

**Verdict: pre-flight is louder than `tpi --version` and ships
basic-auth over a `InsecureSkipVerify=true` channel.**

The probe:
1. `DialTimeout(host:443, 2s)` — passive observers see operator IP.
2. `tls.Dial(InsecureSkipVerify=true)` — handshake is unauth'd; LAN
   ARP-poison / switch-owning attacker MITMs.
3. `GET /` with basic auth — `Authorization: Basic <b64>` rides the
   MITM'd channel. Critic §4.11 — concur.

Required:

- **Document LAN threat posture in `design.md:D3`**, not in a "Why
  box" deferred to the operator guide.
- **TLS pin upgrade path.** Add `boot.tpi.bmc_ca_ref` /
  `bmc_fingerprint`; when set, `InsecureSkipVerify=false`. Schema
  surfaces it; default unchanged.
- **Strict probe ordering**: TCP → TLS → HTTP; abort before
  building `Authorization` if TLS handshake fails.
- **BMC version disclosure.** `ErrBMCVersion.details.bmc_version`
  hidden by default; opt-in via `--verbose`. The version string
  ends up in copy-pasted bug reports.
- **Pre-flight is mutation-only.** `nostos status` MUST NOT run it.
  D8's "uniform `preflight` phase" claim could leak it to read
  paths; restrict explicitly.
- **`ErrBMCAuth` log path** must travel through the v0.2 Scrubber
  seeded with the resolved password. `tasks.md` has no acceptance
  test asserting this; add one.

## 3. Stream B4 — Tailscale device cleanup spoofing

`design.md:D11` (l.369–386), `tasks.md:2.4`.

**Verdict: high-blast-radius mutation gated on a single API field
the device can influence. Insufficient.**

Spoofing scenarios:

1. **Operator clock skew.** `age_days` is `time.Now() - last_seen`
   client-side; a skewed laptop sees every device stale. Refuse if
   local time disagrees with HTTP `Date:` header >5 min.
2. **API hiccup** flips `connected: false` for healthy devices.
   Require two observations 60s apart, both agreeing.
3. **Wrong tailnet.** Dry-run output MUST include `tailnet_name`,
   `device_count_total`, `to_delete`; `--apply` refuses if
   `to_delete > 0.25 * total` without `--force`.
4. **Adversary with tailnet write / hostname-poisoning** toggles
   `connected: false` on a CP device → operator's next `d`
   deletes it. Hard refusal to delete any device whose hostname
   matches a configured node, any role, regardless of state. The
   opt-in `allow-list` is wrong direction; need opt-out
   (`always_keep`) seeded by `nostos/config.yaml`.

Critic §4.4 — the k8s-zombie path is **missing** from D8's `d`
mapping; the only exposed `d` is the higher-blast-radius Tailscale
path. Mandatory adds to `tasks.md:2.4`: confirm prompt lists
hostnames not IDs; TTY check (`--apply` without `--yes` without
TTY exits 2); audit JSONL at
`~/.local/state/nostos/audit/cleanup-<run-id>.jsonl`, mode 0600.

## 4. Stream C — input hardening: injection vectors

`tasks.md:3.7`, `specs/cli-machine-output/spec.md` "Inputs SHALL be
hardened".

**Verdict: rejection list is enumerated, not specified. No central
validator, no fuzz coverage of `op://`.**

**`op://` refs.** Spec rejects "embedded query parameters." Misses:
fragments (`#frag`); authority injection (`op://vault@evil/item`,
pin grammar `op://<vault>/<item>[/<section>]/<field>`); control
chars / NULs (`\x00-\x1f`, `\x7f`) — log/argv injection via
`details.field` echo; percent-encoding (`%2F`) — literal-only;
length cap 1024 against `op inject` exhaustion.

**`--json` / structured-error echo.** No JSON input path in v0.3,
but structured-error `details` echoes raw input. MUST be truncated
and control-char-escaped — terminal is otherwise the sink (ANSI,
OSC 8, bracketed-paste). All string-typed flags: UTF-8, NUL-free,
< 4096 bytes.

**`--fields` masks** (critic §3.6). Dot-notation across arrays can
DoS the projector. Cap depth 3, array len 10000, reject expansion
beyond.

**Path-lock + symlinks.** `tasks.md:3.7` lex-cleans `..`; symlinks
escaping outside the allowed root pass lex-clean. Required:
`os.Lstat` each component post-clean, refuse escaping symlinks.
Same rule for `nostos/docs/<vendor>-<model>.md` (§8).

**No central validator.** Each rule lives in its own test. Required:
`internal/cli/inputs/` with `Validator` per kind (`NodeName`,
`OpRef`, `ConfigPath`, `FieldMask`, `VendorModelKey`); otherwise
dashboard `n`-action and future MCP drift. 5-min nightly fuzz,
not 5-second.

## 5. Stream C8 — MCP deferral

`proposal.md` C8, `tasks.md:7.1`, critic §6.1.

**Verdict: deferral is correct. Possibly the only correct call in
the streams.**

Critic argues MCP is "free" once schema/errors/dispatch/Plan ship.
Free in engineering. **Not free in attack surface.** MCP brings
(a) persistent stdio JSON-RPC inheriting `op`/kubeconfig/Tailscale
OAuth; (b) multiplexed in-flight requests — v0.2's "concurrent
operator" becomes intra-process, per-node flock no longer mediates
two `node install` requests from one client; (c) a new auth model
(model? agent? cleartext?) v0.3 has not specified. Hold for v0.4.
v0.3 MUST NOT ship "preview MCP" or `dashboard --serve` — add an
explicit forbid line to `proposal.md` Non-Goals.

## 6. Stream D dashboard — credential lifetime in long-running TUI

`design.md:D10` (l.360–367), `specs/dashboard/spec.md` Req 1,
`tasks.md:4.1`.

**Verdict: this is the v0.3 issue I'm most worried about.**

Steady-state holdings: Tailscale OAuth bearer (`devices:write` if
`d` reachable); kubeconfig + Talos client cert/key; BMC creds
cached if `r` reachable; `OP_SESSION_*` in env, inherited by
children (v0.2 §10). Operator walks away → anyone passing gets
one-keystroke `r`/`d` plus on-screen topology. Snapshot file mode
**unspecified** (`design.md:D10`, `tasks.md:4.9`).

Required:

- **Idle lock**: default 10 min (configurable, ≥1) no-input →
  blur pane, re-confirm, zero in-memory tokens. Wake re-resolves
  via `op`.
- **Token zeroization on quit**: `tea.Cmd` shutdown overwrites
  bearer/cert bytes (`subtle.ConstantTimeCopy`). GC isn't enough.
- **Snapshot scrubbing**: pure cluster-state; no creds, no resolved
  `op://`. Golden test rejects field names matching
  `(?i)token|secret|password|key|bearer`.
- **Snapshot mode 0600 in 0700 dir** (v0.2 §6).
- **Action audit log**: every `r`/`d` appends to
  `~/.local/state/nostos/audit/dashboard-<session>.jsonl` —
  `{at, key, target, dispatched_command, exit_code}`.
- **OAuth scope minimization**: default read-scoped; prompt for
  write-scoped only on `d`.
- **Refuse on remote pty** without explicit flag (`SSH_CONNECTION`)
  — pty buffer leak is worse.

## 7. Stream D action handlers — `r` reinstall, `d` delete

`design.md:D8` (l.269–303), `specs/dashboard/spec.md` Req 4,
`tasks.md:4.7`, critic §4.4.

**Verdict: no consent flow, no audit log, no fat-finger guard.**

`r` invokes `nostos node install --reinstall <name>` through
dispatch. For a controlplane node this wipes etcd, breaks quorum,
disrupts ArgoCD apps. No spec text requires:

- Plan shown before mutation.
- Typed confirmation (e.g., type the node name).
- Refusal on `role == controlplane` without `--allow-controlplane`.

`d` maps to `cluster cleanup --apply` "scoped" — "scoped" is
undefined (critic §4.4). Worst plausible reading: "delete the
selected row" — one-keystroke cluster degradation (combine with §3).

Required additions to `specs/dashboard/spec.md` Req 4: `r` displays
Plan, requires typed node-name confirm, refuses on controlplane
without override key; `d` displays Plan, Tailscale requires typed
hostname, k8s path (missing — fix per §3) refuses configured
names; both append audit; `Esc` cancels, default-Y forbidden; UX
test `r<Enter><Enter>` MUST NOT mutate. Cross-ref critic §2.1:
dispatch exposes per-node `RunState`; `r` refused if `InProgress`.

## 8. Stream D living docs — markdown rendering injection

`design.md:D9`, `specs/dashboard/spec.md` Living-doc requirement,
`tasks.md:4.10–4.12`.

**Verdict: SQLi is the wrong analogy; the real risk is
terminal-escape injection through Glamour/Lipgloss.**

Glamour has had multiple CVEs on terminal-escape passthrough.
`nostos/docs/` is repo-tracked → malicious PR → operator opens
dashboard → renders attacker content. Vectors: `\x1b[2J` to clear
screen and overwrite UI; OSC 8 hyperlinks impersonating vendor
links; bracketed-paste bypass; raw HTML.

Required: strip ANSI escapes (`\x1b[`, `\x1b]`, `\x1b\\`) before
Glamour parses; OSC 8 disabled or `https://` + domain allow-list,
default off; file size cap 1 MiB; HTML / image disabled in
renderer; path resolution chases symlinks (per §4), refuses files
outside `nostos/docs/`; `<vendor>-<model>` from `nostos/config.yaml`
(critic §5.12 flags as undefined — fix AND constrain to
`^[a-z0-9][a-z0-9-]{0,62}$`); `tasks.md:4.10` golden test fixture
with `\x1b[2J`, OSC 8, raw HTML, 1 MiB+1 — must sanitize.

## 9. Stream E — what should NOT be in the public guide

`tasks.md:5.1–5.10`, `docs/nostos-guide.md` (NEW).

**Verdict: guide WILL leak operator-specific data without redaction
in the doc-build.**

MUST NOT contain:

- Specific LAN IPs (192.168.68.100/.104/.107/.114 in `CLAUDE.md`)
  — use `<controlplane-ip>`. Repo is public.
- Tailscale 100.x addresses.
- BMC default user/pass (§5.5) — reference `op://vault/turingpi-bmc`
  only.
- OAuth client IDs (§5.7) — paired with rotation guide enables
  targeted phishing. Screenshots must not show client_id/secret
  even if expired.
- Operator-specific 1Password vault/item names; use generic
  `op://homelab/turingpi-bmc`.
- Schematic IDs — reveals exact extension surface (NVIDIA driver,
  Tailscale extension versions).
- Kubeconfig CA/cert/token bytes.
- Tailnet name.
- Auto-generated `nostos schema` reference (`tasks.md:5.10`) MUST
  run against `examples/config.yaml`, not the operator's actual.

Required `docs:lint` task that greps the guide for
`192\.168\.\d+\.\d+`, `100\.\d+\.\d+\.\d+`,
`(?i)client[_-]?(id|secret)`, and any literal from
`nostos/config.yaml::nodes[*].name`. CI fails on match.

The critic's §3.7 (ship turing-rk1 only) aligns: fewer playbooks →
less surface for accidental leakage.

## 10. Inherited v0.2 gaps still open

The v0.2 review listed seven mandatory items (§12). v0.3 addresses
**none**:

1. EventEmitter Scrubber ownership — silent.
2. ApplyConfigInsecure double-exposure on PXE — silent.
3. Maintenance-mode peer identity verification — silent.
4. tpi version pin + key-file lifecycle — silent.
5. Image sha256 source of truth — partial via D2; build-tag
   unwritten in tasks.
6. **Tailscale authkey lifecycle** (single-use, TTL, rotation) —
   silent. v0.3's `cluster cleanup` deletes *device records*; says
   nothing about authkey rotation. `secrets keys revoke` is
   v0.4 (`tasks.md:7.4`), so v0.3 ships zero authkey hygiene.
7. Per-node `flock` spec scenario — silent, even though the
   dashboard now reads cluster state while installs may be in
   flight (critic §2.1).

v0.3 MUST either re-confirm these from v0.2 or accept as known
debt with v0.4 owners. Right now `design.md:9-13` pretends v0.2
"acceptance was met" and silently inherits the gaps.

## 11. Spec edits required before implementation

1. Dashboard idle lock + token zeroization (§6).
2. Action consent + audit log (§7).
3. `cluster cleanup` controlplane refusal + clock-skew check (§3).
4. Markdown render sanitization (§8).
5. `op://` grammar + central validator (§4).
6. TLS-pin upgrade path for BMC pre-flight (§2).
7. Image-cache `fsync` + `*.part` flock + named TOFU build tag (§1).
8. Doc-build leak lint (§9).
9. Re-affirm v0.2 mandatory items in `proposal.md` Non-Goals (§10).

## 12. Five questions for the leader

1. Is the dashboard's credential cache (OAuth + kubeconfig + BMC +
   `op` session in one process) acceptable for v0.3, or must idle
   lock + token zeroization land before TUI ships?
2. Should `cluster cleanup --apply` be reachable from the TUI in
   v0.3, or CLI-only until controlplane refusal + audit log land?
3. The v0.2 mandatory items (Tailscale authkey lifecycle, Scrubber
   sink, peer identity verification) — accept as v0.4 debt
   explicitly in `proposal.md` Non-Goals, or block v0.3?
4. Living-docs rendering: ship Glamour with sanitization pre-pass,
   or defer the pane to v0.4 (matches critic §3.7)?
5. Doc-build leak lint: positive allow-list (only certain
   placeholders permitted) or deny-list of patterns? Allow-list
   is safer; deny-list is what v0.3 can ship.
