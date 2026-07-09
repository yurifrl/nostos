# nostos v0.3 — critic review

Reviewed:
- `openspec/changes/nostos-v03-dashboard-and-hygiene/{proposal,design,tasks}.md`
- `openspec/changes/nostos-v03-dashboard-and-hygiene/specs/{dashboard,cli-machine-output}/spec.md`
- `v03-{brief,summary}.md`

**Headline:** v0.3 as written is two releases in a trench coat with a TUI on
top. Stream A (bugs) and B (hygiene) are real and shippable. Stream C is half
a CLI rewrite. Stream D is a small product. Stream E is a book. The "MVP"
framing on D and the "five streams, all v0.3" framing in the proposal are
aspirational, not engineering. Half the v0.3 surface depends on v0.4 commands
that don't exist yet. Three concrete spec defects must be fixed before code
(§9).

------------------------------------------------------------------------------

## 1. Scope: this is a v1.0 disguised as cleanup

`proposal.md:1-30` claims v0.2 shipped and v0.3 "closes the v0.2 bug list."
Then it bolts on:

- a 174-line CLI machine-output capability (`specs/cli-machine-output/spec.md`)
- a 152-line interactive TUI capability (`specs/dashboard/spec.md`)
- a 10-section operator guide (`tasks.md:5.1-5.10`)
- per-vendor playbooks for four hardware platforms (`tasks.md:4.11`)
- two new mutating CLI subcommands (`cluster cleanup`, `docs init`)

`tasks.md` has 51 numbered items. For a homelab tool that just shipped its
first real provisioner abstraction. The summary's own Question 1
(`v03-summary.md:90-94`) admits the worry. The brief's "non-negotiable"
roadmap label (`v03-brief.md:165-168`) is the tell: when a planner writes
"non-negotiable" in their own brief, the scope is what's actually getting
negotiated.

------------------------------------------------------------------------------

## 2. The concurrency holes you asked about

### 2.1 Dashboard while `nostos node install` is running — undefined

`design.md:D8` says every dashboard action goes through
`internal/cli/dispatch/`. v0.2 already has a per-node flock. So `r` collides
with a running install via flock — fine. **Read-side probes are not gated by
anything:**

- `design.md:D7` runs ICMP/ARP/mDNS/Talos-maintenance/ArgoCD against every
  node every 5–30 s.
- A node mid-flash sits in **Talos maintenance mode** on TCP 50000 with no
  hostname, no kubelet, no machine config applied.
- `specs/dashboard/spec.md` Requirement 2 classifies devices as
  `known`/`orphan`/`unknown`. A node mid-flash matches by MAC and shows up
  `known` but **fails every health check** (kubelet not Ready, schematic
  doesn't match, Talos version probe times out).
- The aggregate state machine (`specs/dashboard/spec.md` Requirement 1)
  flips to `BROKEN` the moment any check returns severity=error. **A normal
  reinstall flips the dashboard red.**

There is no `installing`/`transient` state in the bucket vocabulary
(`design.md:D7`, `specs/dashboard/spec.md`) and no concept of "a sibling
process is operating on this node." The dispatch seam doesn't publish
per-node state back to the dashboard. The flock file exists but no consumer
reads it.

This is the single biggest gap in Stream D. Fix requires:

- A `RunState` registry exposed by the dispatcher.
- Health checks consulting that registry and demoting severity from `error`
  to `info: in-progress` for nodes under active mutation.
- A fourth aggregate state (`TRANSITIONING`) or the contract lies.

`v03-summary.md:106-109` asks reviewers about a "stale data" state. Wrong
question. The missing state is **"a peer is changing this."**

### 2.2 Discovery picks up a node mid-flash — also undefined

A box halfway through PXE/`tpi flash` may briefly:
- Respond to ICMP from iPXE (no MAC stability vs final OS).
- Open TCP 50000 with maintenance apid for ~60 s, drop, return.
- Advertise mDNS as something the operator never configured.

`specs/dashboard/spec.md` Requirement 2 specifies MAC > IP > Tailscale match
but **no debounce or hysteresis**. A box flickering on/off TCP 50000 during
flash will appear and disappear from the `unknown` bucket every 5 s. The
snapshot file (`design.md:D10`, `tasks.md:4.9`) persists "after every
successful slow-tier refresh" and can capture either flickering state.

Also undefined: a single physical box publishing two identities during one
sweep (BMC MAC on one IP, Talos MAC on another). The MAC>IP rule treats
them as two devices. Match-priority decisions belong in spec, not impl.

### 2.3 `--dry-run` on `node install` — handwaved

`tasks.md:3.3` and `specs/cli-machine-output/spec.md` "Dry-run install plans
every phase" promise a Plan covering all six phases (`preflight`, `prepare`,
`boot`, `wait`, `apply`, `cleanup`) **and** "the subprocess seam
(`Commander`) records ZERO invocations." Pick one:

- `preflight` is itself a network probe (`design.md:D3`: TCP+TLS+HTTP).
  Skipping it makes the plan lie (apply-time preflight failure isn't
  previewed). Running it produces real BMC traffic and auth log entries —
  not "zero invocations."
- `prepare` includes image cache `Ensure` (`design.md:D2`). Downloading 600
  MB is a side effect; skipping it leaves the planned digest unknown and a
  real `--apply` may discover digest mismatch (exit 7) for a node the
  preview said was ready.
- `boot` for tpi is `tpi power reset`. There is no "dry" form. PXE can't
  even guess when the operator presses the button.
- `wait` is observational — dry-run produces no info.
- `apply` is `talosctl apply-config`, no dry-run upstream.

So `--dry-run` on `node install` is one of:
- **plan-only, no probes**: cheap, lies, admits no validation.
- **plan + preflight + image fetch**: side-effecting and misnamed.
- **plan + preflight only**: defensible middle ground, not what the spec
  says.

The proposal must pick one and document the limits. As written, the dry-run
contract is a label, not an interface.

------------------------------------------------------------------------------

## 3. Premature design / over-engineering

### 3.1 Four-tier refresh cadence
`design.md` Definitions ("Refresh tier") and `tasks.md:4.5` mandate 200 ms /
5 s / 30 s / 5 min. For a 4-node cluster. ICMP cap-32 fanout on a `/24`
takes ~8 s wall clock (`design.md:D7`); doesn't fit "medium=5s." mDNS
window is 3 s; doesn't fit anywhere. The whole tier system is invented
before the simplest version (one timer) has been measured.

### 3.2 Hardcoded check registry "with clean seam for v0.4 plugins"
`design.md:D5` ships a typed `CheckID` enum **and** documents a plugin
upgrade path. YAGNI applied wrong: hardcode it or abstract it, not both.
Also: 12 builtin checks, several redundant (`CheckPerNodeICMP` vs
`CheckPerNodeApid` — if apid responds, ICMP is implied modulo firewall).

### 3.3 Snapshot-to-disk for <100 ms cold start
`design.md:D10`, `tasks.md:4.9`. For a tool an operator runs maybe twice a
week. The 100 ms target is theatre — discovery results are stale by the
time you see them, so showing them faster isn't a win, it's a more
confident lie.

### 3.4 Image-cache fix has admitted runtime tax
`design.md:D2` explicitly says "on every cache-hit re-hash the file
(cost: ~2 s for a 600 MB image)." The kill-9 window the design defends
against is **already bounded to a single Ensure call**; rehash-on-read
buys detection of third-party tampering that wasn't in the bug report. The
real bug is "kill-9 mid-download leaves a partial file"; the fix is
write-`*.part`+fsync+atomic-rename+atomic-tempfile-write of `digests.json`.
The 2 s tax is permanent and unjustified.

### 3.5 Reflection schema + side-table for enum/validation
`design.md:D6`. Two sources of truth per flag (cobra metadata +
`schema/annotations.go`). `tasks.md:6.4` catches *missing* entries but not
*stale* enum values when a flag adds a value annotations.go missed. Drift
is inevitable; you'll find it the day an agent submits a real enum value
the schema doesn't list. One source: a thin `cobra.Flag` wrapper that
takes enum/validation directly.

### 3.6 Field-mask dot-notation across arrays
`specs/cli-machine-output/spec.md` "List and show ... `--fields`" requires
"dot-notation for nested objects and arrays (e.g. `nodes.name`)." This is
jq-lite (does `nodes.name` mean "every node's name" or "field 'name' of
object 'nodes'"?). Top-level fields are enough for v0.3; the dot-notation
is feature-creep no agent has asked for.

### 3.7 Per-vendor playbooks × 4
`tasks.md:4.11`, `design.md:D9`. Six headings, four vendors, Lipgloss
markdown rendering. The repo runs three of these (no current rpi-5 — the
rpi controlplane was decommissioned per `CLAUDE.md`). Writing a playbook
for hardware you don't have is documentation cosplay. Ship turing-rk1
only and let the structure prove itself.

### 3.8 12-entry exit-code catalog
`design.md:D12`. 12 codes for ~6 leaf commands. Several (4 = node already
ready, 7 = digest mismatch, 9 = dependency missing) are highly specific to
one command. Half will be unused for two years. Ship 0/1/2/5 in v0.3.

------------------------------------------------------------------------------

## 4. Internal inconsistencies (citations)

### 4.1 Exit code 10 collision
- `design.md:D12`: code `10 = network unreachable`.
- `tasks.md:4.8` and `specs/dashboard/spec.md` Headless scenario:
  `--exit-nonzero-on-broken` flips to **10** on `BROKEN`.

A CI script can't distinguish "network down" from "cluster degraded."

### 4.2 Dry-run exit code 8 breaks shell semantics
`design.md:D12` and `specs/cli-machine-output/spec.md`: code 8 = "dry-run
preview returned." A *successful* dry-run is now non-zero. `nostos foo
--dry-run && echo ok` will never echo ok. CI tooling (most of it) treats
non-zero as failure. POSIX 0 means "the program did what you asked"; the
user asked for a preview, the preview was produced, that is success.
Distinguishing preview vs apply belongs in `status: "preview"` JSON
output, not the exit code.

### 4.3 `nostos cluster upgrade` referenced in v0.3, doesn't exist
- `design.md:D8` action table maps `u` → `nostos cluster upgrade --dry-run`.
- `proposal.md` Non-Goals: "Not `cluster upgrade`. Mutation lands in v0.4."
- `tasks.md:7.3`: mutation is v0.4.

So v0.3 ships a half-command (`upgrade --dry-run` exists, `--apply`
doesn't). The action contract D8 ("every action routes through dispatch")
forces this awkwardness. Either ship the command fully or have the
dashboard's `u` produce an inline diff and not call any CLI surface.

### 4.4 `d` action diverges between brief and design
- `v03-brief.md:120-122`: `d` for k8s zombies → `kubectl delete node`; for
  Tailscale stale → `nostos secrets keys revoke + tailnet device delete`.
- `design.md:D8` table: `d` → `nostos cluster cleanup --apply (scoped)`.
- `design.md:D11`: `cluster cleanup` is **Tailscale-only**.

The k8s-zombie path silently disappeared. `tasks.md:2.3` documents
`kubectl delete node talos-76w-r75` as a manual one-shot — meaning the
dashboard's `d` action **cannot fix the very motivating example** from
`proposal.md`'s "Concrete pains" list. Also: what does `(scoped)` mean?
Single device? Selected row? Whole >7-day-offline set? Unspecified.

### 4.5 Brief's `task turing:install` alias dropped
`proposal.md` Stream B1 promises `task turing:install NODE=<name>` as an
alias. `tasks.md:2.1` only deprecates `flash`/`download`/`install-talos`/
`get`. Alias creation is unaccepted. Either it's in scope or it isn't.

### 4.6 `nostos node install tp1 && nostos node install tp4` regresses parallelism
`tasks.md:2.2` Acceptance: sequential install. The pre-existing
`task talos:apply` likely fired both in parallel (talosctl is happy to).
Sequential doubles wall time per cluster bring-up. Workflow regression
labelled as hygiene.

### 4.7 `nostos doctor` cited but doesn't exist
- `tasks.md:3.8`: AGENTS.md says "run `nostos doctor` before any install"
  with a parenthetical "(note: `doctor` is v0.4; AGENTS.md says so)."
- `tasks.md:7.5`: `doctor` is v0.4.

AGENTS.md (the document teaching agents how to drive v0.3) tells them to
run a v0.4 command. Either ship a stub now or remove from AGENTS.md.

### 4.8 Stderr "always" non-empty for hints contradicts JSON contract
- `specs/cli-machine-output/spec.md` "`nostos status` emits JSON" scenario:
  "stderr is empty under success."
- Same spec, "Errors SHALL be structured": "Hints SHALL always go to
  stderr."

A successful command emitting a hint (e.g., "tp1 already ready, use
`--reinstall`") makes stderr non-empty *under success*. The two scenarios
disagree.

### 4.9 `nostos docs init` referenced, not defined
`specs/dashboard/spec.md` "Living-documentation pane" missing-playbook
scenario references `nostos docs init`. `tasks.md:4.12` mentions it
creates "a stub from template." What template? Where? `tasks.md:4.11`
ships fixed playbooks but no template. Underspecified.

### 4.10 24h `*.part` GC vs slow downloads
`tasks.md:1.4`: GC of `*.part > 24h` runs at start of every Ensure. On
slow links a 600 MB image plus retries can exceed 24h, and a peer Ensure
call would then delete the in-flight file. Add a lock on the `.part`
filename and document it.

### 4.11 BMC TLS-skip-verify
`design.md:D3`: `tls.Dial(... InsecureSkipVerify=true)`. Credentials go
through that connection; a LAN attacker can MITM. For the canonical
operator guide this needs an explicit "Why" box, not relegation to
`v03-summary.md` Q2.

### 4.12 ICMP fanout time vs medium tier
`design.md:D7`: cap 32, 1 s timeout. `/24` worst case ~8 s. Definitions
"Refresh tier" places ICMP at "medium (5 s)." Math doesn't fit. Either
sliding-window the sweep or move ICMP to slow tier.

------------------------------------------------------------------------------

## 5. Under-specified parts

1. **Dashboard visibility into running mutations.** No state surfacing
   from `dispatch` to TUI. (`design.md:D8`, `specs/dashboard/spec.md`
   Req 1.) See §2.1.

2. **Discovery debounce / hysteresis.** Time-series dedup unspecified.
   (`design.md:D7`, `specs/dashboard/spec.md` Req 2.)

3. **Multi-interface laptops.** Which NIC for mDNS? Which `/24` for ICMP?
   `nostos/config.yaml` declares cluster network but laptop may have
   several. (`design.md:D7`.)

4. **Snapshot/`dashboard.toml` schema versioning.** No version field;
   corruption + forward-compat unaddressed. (`design.md:D4`, `D10`.)

5. **Headless `--once` total runtime budget.** No deadline cap; degraded
   network multiplies per-probe timeouts. Matters for cron.
   (`specs/dashboard/spec.md` Headless requirement.)

6. **`cluster cleanup --apply` confirmation under `--output json`.**
   `tasks.md:2.4` says "requires confirmation (or `--yes`)." TTY check?
   Auto-fail without `--yes`? Unspecified.

7. **OAuth scope failures.** `cluster cleanup --apply` calling
   `DELETE /api/v2/device/...` without delete scope — error code?
   (`design.md:D11`.)

8. **Symlink escapes in `--config`.** `tasks.md:3.7` rejects `..` after
   lex-cleaning, but a symlink under home/repo pointing outside is a legal
   lex result. Not addressed.

9. **PXE preflight.** A3/D3 spec BMC preflight; no analogous PXE preflight.
   But `design.md:D8` Plan promises uniform `preflight` phase for both.

10. **`nostos node identify` for PXE.** `design.md:D8`: `i` = "visible-only;
    UART echo or LED blink." For x86 PXE box with no BMC, brief D6
    hand-waves "blink the network LED via traffic burst." What command?

11. **Match priority collisions.** Two devices with same MAC (VM bridges,
    BMC + host on same chassis) — collision behavior unspecified.
    (`design.md:D7`.)

12. **Living-docs path resolution.** `nostos/docs/<vendor>-<model>.md` —
    `<vendor>`/`<model>` come from where? `nostos/config.yaml` doesn't
    carry these fields in v0.2 schema. Adding them is a config-schema
    change not enumerated in tasks. (`specs/dashboard/spec.md` Living-doc
    requirement.)

13. **Stale-snapshot threshold.** Cold start renders last-known state in
    <100 ms; no max age before "do not display." A 2-week-old snapshot is
    misleading. (`tasks.md:4.9`, `design.md:D10`.)

14. **Per-app health for 30-app clusters.** `CheckPerAppHealth` per
    Application is 30 checks; aggregate-state weighting unspecified.
    (`design.md:D5`.)

------------------------------------------------------------------------------

## 6. Bad roadmap bets

### 6.1 Deferring MCP while building everything MCP needs
`tasks.md:7.1` defers MCP to v0.4. But v0.3 builds: structured errors with
stable codes, schema introspection, shared dispatch seam, typed Plan,
JSON output everywhere. **That is the MCP server**, minus a JSON-RPC-over-
stdio wrapper. The deferral saves perhaps a week; you've already paid the
design tax. Either ship MCP in v0.3 or admit the deferral is bookkeeping.

### 6.2 Living docs before live install observability
The dashboard ships markdown rendering for vendor playbooks
(`tasks.md:4.10-4.12`) before any view of an install in progress (§2.1 —
install state is invisible to the TUI). Priorities upside down. An
operator running `nostos node install tp1` from another shell wants the
dashboard to say "tp1: installing, phase=apply" — not to read the
turing-rk1 playbook again.

### 6.3 Auto-generated CLI reference from `nostos schema`
`tasks.md:5.10`: doc-build step regenerates the reference table; CI fails
on divergence. Living-docs pipelines are graveyards. This will diverge in
week two and the gate will be either disabled (lose the docs) or pinned
and ignored (lose the contract). Generate at release time, not per PR.

### 6.4 Aggregate state with no operations dimension
`ALL_GREEN`/`DEGRADED`/`BROKEN`. No fourth state for "operation in
progress." See §2.1. First thing operators will ask for; baking it in
later is a spec break — `aggregate_state` is declared "part of the public
contract" (`specs/dashboard/spec.md` Headless requirement).

### 6.5 Headline contradicts Non-Goals
`v03-brief.md:74` and `proposal.md` Stream D promise an operator who has
never touched the lab can run `nostos dashboard` and be **guided** to all
green. But `proposal.md` Non-Goals: "Not a guided fix dispatcher."
`tasks.md:7.2` defers guided-fix flows to v0.4. Either tighten the
headline or pull guided-fix into v0.3.

### 6.6 Ship four vendor playbooks for hardware you don't have
`tasks.md:4.11`: dell-optiplex-3080m, turing-rk1, generic-amd64,
raspberry-pi-5. Rpi-5 controlplane was decommissioned (`CLAUDE.md`).
Documentation cosplay. Ship the two operated hosts.

------------------------------------------------------------------------------

## 7. Acceptance theater

- `tasks.md:1.2`: "tp1 install end-to-end without manual `talosctl
  apply-config`." Gated on a magic 30-min number that `design.md:Q1`
  admits is unmeasured. Slow firmware → acceptance flake; bug is in the
  constant.

- `tasks.md:5.1`: "every command in the guide runs (shellcheck'd)."
  Shellcheck is a syntax linter. It won't catch missing subcommands or
  flag spelling drift. Real acceptance: every command parses against
  `nostos schema`. Trivially implementable; not done.

- `tasks.md:6.4` (schema completeness): catches missing entries, not
  stale ones (§4.10 inverse — same drift problem).

- `tasks.md:4.7` (dispatcher contract): validates `r` calls dispatcher.
  Does **not** validate that aggregate state during the run reflects an
  in-progress install. (§2.1.)

- `tasks.md:4.9` (snapshot <100 ms): UX test that mocks the filesystem
  and measures decode time. Not a meaningful operator UX assertion.

------------------------------------------------------------------------------

## 8. What I'd cut for v0.3 (opinion)

If you trust Stream A and B, ship those fast and stop bundling.

**Keep ("nostos 0.3: cleanup"):**
- A1, A2, A3, A4 — bugs.
- B1, B2, B3 — taskfile retirement. Drop B4 until k8s-zombie is also
  automated; partial cleanup is worse than none.
- C1 (`--output json` on existing commands), C5 (structured errors), C7
  (AGENTS.md). Drop "every mutation gets `--dry-run`" — promise it on the
  one mutation that exists (`node install`) only after picking a posture.

**Push to v0.4 ("nostos 0.4: agents"):**
- C2 (`schema`), C3 (`--fields`), C4 (typed Plan).
- MCP server (free at that point).
- Stream D entirely.
- Stream E entirely; replace with a 1-page README update.

**Push to v0.5+ / v1.0:**
- Living-docs in TUI, per-vendor playbooks, auto-generated CLI reference.

------------------------------------------------------------------------------

## 9. Three things to fix before approval

1. **Dashboard behavior during a concurrent `nostos node install`.**
   New state, in-flight-op registry, or explicitly documented lying.
   (`specs/dashboard/spec.md` Req 1, `design.md:D8`.)

2. **`--dry-run` posture for `node install` with limits documented.**
   The current "every phase planned" + "ZERO subprocess invocations" is
   incompatible. (`specs/cli-machine-output/spec.md` Dry-run install
   requirement.)

3. **Resolve exit-code 10 collision and reconsider exit 8 for dry-run.**
   (`design.md:D12`, `tasks.md:4.8`, `specs/dashboard/spec.md` Headless
   scenario.)

Everything else is fixable in PR. Those three are spec defects and need to
land before code.
