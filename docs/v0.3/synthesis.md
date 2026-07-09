# nostos v0.3 — review synthesis

Folds four reviews (critic, security, ux, tests) into the OpenSpec
change at `openspec/changes/nostos-v03-dashboard-and-hygiene/`.
Updated files: `proposal.md`, `design.md`, `tasks.md`,
`specs/dashboard/spec.md`, `specs/cli-machine-output/spec.md`.

## What got CUT

| Cut                                                  | Reason                                                                                  |
|------------------------------------------------------|-----------------------------------------------------------------------------------------|
| "Guided fix dispatcher" north-star framing           | Critic §6.5 + UX §1: the headline contradicts every Non-Goal. Reframed as "live status board, not autopilot". |
| 4-tier refresh cadence (200 ms / 5 s / 30 s / 5 m)   | Critic §4.4: 4 tiers solve no concrete problem. Collapsed to **fast 5 s** + **slow 5 min**. UI keystrokes ride the Bubble Tea event loop. |
| `~/.cache/nostos/dashboard-state.json` cold-start cache | Critic §4.4: Bubble Tea cold-start is fast; 100 ms snapshot replay is theatre. Removed. Only `upstream-versions.json` (24 h TTL) remains \u2014 it's a network-cost optimization, not a UI optimization. |
| "Hardcoded but plugin-ready" check registry seam     | Critic §6: pick one. Picked **hardcoded**. v0.4 redesigns with concrete use cases. |
| Vendor playbooks `generic-amd64` + `raspberry-pi-5`  | Critic §4.5: don't ship docs for hardware we don't run. v0.3 ships **`dell-optiplex-3080m`** and **`turing-rk1`** only. |
| 12-entry exit-code catalog                           | Critic §4.2 + Tests §7: collapsed to **6** entries (success / generic / validation / network / auth / conflict). |
| Stream B4 Tailscale device cleanup mutation         | Security §5: tailnet spoofing concerns require allow-list + tagged-namespace + two-keystroke confirm to be safe. v0.3 documents the manual workflow only. |
| Dashboard action handlers `r`, `d`, `i`              | UX §1 + scope discipline: v0.3 ships READ-ONLY MVP; action dispatch lands in v0.4. |
| Operator-guide Sections 4 / 5 / 6 in v0.3           | Critic + scope: PXE+TPI flows merged into Section 3; per-vendor playbooks (\u00a75) and Tailscale OAuth deep-dive (\u00a76) deferred to v0.4. |

## What got TIGHTENED

### A2 — TOFU image cache (security)
Two-phase commit made explicit: temp file → atomic rename → digest
record + fsync in the same critical section. **Temp files at startup
are garbage; deleted unconditionally.** No resume-from-`*.part`
heuristic (it would introduce a TOCTOU between byte-count and hash
checks). On cache hit, recompute sha256 before trusting any record;
mismatched record → delete file and redownload.

### A3 — BMC pre-flight (security)
Probes are scoped strictly to the **configured** BMC host (no
internal-IP discovery, no `/24` walking — those are LAN-scanning
fingerprints). Token-bucket rate-limit: at most one probe per host
per 5 s; failures back off 5 s → 30 s → 5 min. The dashboard does NOT
include BMC pre-flight in either refresh tier; it runs only as part
of `node install` or explicit `nostos node check`.

### C6 — Input hardening (security)
- Reject **all** ASCII control chars (`0x00-0x1F` + `0x7F`) in any
  user-supplied string, not just node names.
- Path validation rejects `..` after lex-clean AND any path
  resolving outside `$HOME` / repo root **after symlink resolution**.
- 4 fuzz targets at 5 s budget each (`FuzzNodeName`, `FuzzOpRef`,
  `FuzzConfigPath`, `FuzzFieldMask`).

### D — Living-docs renderer (security)
- Playbooks ship as **`embed.FS`** in the binary. No filesystem
  read for the base content at runtime. Operator overlays at
  `nostos/docs/<v>-<m>.md` merge on read.
- Glamour configured in **strict-ANSI mode** with raw-HTML
  rejection \u2014 ANSI injection from operator overlays is neutralized,
  `<script>`/`<iframe>` tags are visibly elided.

### E — Operator guide (security)
Explicit **"do not commit" list** at the top of the guide:
home-network IPs, BMC default credentials, OAuth client IDs and
secrets, MAC addresses, `op://` vault paths, kubeconfig client
certs.

### Aggregate state semantics (UX)
- 5 states, not 3: `ALL_GREEN`, `DEGRADED`, `BROKEN`,
  `UNCONFIGURED`, `TRANSITIONING`.
- Empty cluster → `UNCONFIGURED` + 4-step CTA, **never green**.
- Mid-reflash node → `TRANSITIONING` (flock held), not `BROKEN`.
- Missing kubeconfig → top-level warning row, never silent skip.

### Symbols & accessibility (UX)
`✓ ⚠ ✗ ?` for color terminals; bracket variants
`[OK] [WARN] [FAIL] [?]` under `NO_COLOR=1` / non-TTY / `--ascii`.
Color is never the sole carrier of state.

### Keymap (UX)
- Capital `H` (not lowercase `h`) for show-hidden toggle \u2014 no
  collision with potential `h` hide.
- Capital `G` opens the relevant guide section read-only in v0.3.
- `?` help is **curated** prose, decoupled from `nostos schema`
  output.
- Contextual footer: unknown row → `[n]ame`; orphan/known →
  read-only in v0.3.

### Identify ordering (UX, deferred to v0.4)
When `[i]dentify` ships, ordering is **Redfish chassis-LED → NIC
packet flood → `tpi` UART** (most visually unambiguous first).

## What got DEFERRED to v0.4

- B4 \u2014 `nostos cluster cleanup` for Tailscale offline devices.
- Dashboard action handlers `[r]einstall`, `[d]elete`, `[i]dentify`.
- Guided-fix dispatcher behind capital `G`.
- MCP server surface (Stream C8).
- `cluster upgrade --to <ver>` mutation (v0.3 ships preview only).
- `secrets rotate` (Tailscale authkey rotation).
- `nostos doctor`.
- Vendor playbooks `generic-amd64` and `raspberry-pi-5`.
- Operator-guide Sections 5 (per-vendor playbooks) and 6 (Tailscale
  OAuth deep-dive).
- Plugin / pluggable check registry.
- JSONL run log + `--resume`.
- `inventory.db` (SQLite).
- Drift detection + `nostos diff <node>`.

## Spec defects FIXED (per tests review)

1. **TRANSITIONING aggregate state.** Was missing; a single offline
   worker mid-reflash incorrectly read as `BROKEN`. Now: aggregate
   is `TRANSITIONING` when any per-node flock is held.
2. **`--dry-run` posture.** Now precise: ZERO subprocess invocations,
   output JSON contains `would_execute: [...]`, re-run without
   `--dry-run` produces an execution sequence that is a
   (sub)sequence of `would_execute`. Exit code is 0 with payload
   `status:"preview"` (not a dedicated dry-run exit code).
3. **Exit-code 8 collision.** Eliminated. New 6-entry catalog uses
   the **10-19** range for nostos-specific codes, leaving the low
   single digits to shells. Sub-causes (BMC unreachable vs auth vs
   version, digest mismatch vs unpinned) live in `details.code`,
   not in unique top-level exit numbers.
4. **Empty-cluster state.** Documented explicitly: aggregate
   `UNCONFIGURED`, body replaced by 4-step CTA, never `ALL_GREEN`.
5. **Missing-kubeconfig state.** Documented explicitly: top-level
   warning row; non-kubeconfig probes still run; kubeconfig-
   dependent checks return `severity:warn,
   reason:"kubeconfig_unavailable"` (no silent skip).

## Updated open questions (design.md)

- **Q1** Per-firmware MaxWaitMaintenance map → **RESOLVED-DEFER** to
  v0.4 if measurements warrant.
- **Q2** Kubeconfig missing → **RESOLVED**: top-level warning row,
  trust the operator wrapper (don't mirror kubectl-context guard).
- **Q3** mDNS dependency → **RESOLVED**: acceptable, pin in PR.
- **Q4** Guided-fix `g` mode → **RESOLVED**: read-only in v0.3,
  mutation in v0.4.
- **Q5** ICMP raw-socket privilege → **RESOLVED**: documented; no
  silent ARP-only fallback.
- **Q6** `cluster cleanup` vs `secrets keys revoke` boundary →
  **RESOLVED-DEFER** alongside B4 to v0.4.
- **Q7** Headless exit code → **RESOLVED**: default 0; opt-in
  `--exit-nonzero-on-broken` returns 11/13 keyed off dominant
  failure class.
- **Q8** `--fields` on headless `--once` → **RESOLVED**: yes.
- **Q9** Per-vendor docs location → **RESOLVED**: `embed.FS` base
  + operator overlay; submodule keeps `AGENTS.md` only.

## v0.3 scope (one page)

**Streams shipped in v0.3:**

| Stream | Scope                                                         |
|--------|---------------------------------------------------------------|
| A      | All four v0.2 bugs (1.1\u20131.8). MaxWaitMaintenance, TOFU two-phase commit, BMC pre-flight + rate-limit, regression test. |
| B1\u2013B3 | Taskfile retirement + zombie node doc step. **B4 deferred.** |
| C1\u2013C7 | AI-CLI hardening: `--output json`, `nostos schema`, `--fields`, `--dry-run` (zero-subprocess), structured errors (10-19 codes), input hardening (control chars 0x00-0x1F+0x7F, symlink-resolved path checks), AGENTS.md. **C8 (MCP) deferred.** |
| D      | **READ-ONLY** dashboard MVP: discovery, hardcoded check registry, 5 aggregate states, fast/slow refresh tiers, `--once --output json` headless, embedded living docs (`dell-optiplex-3080m` + `turing-rk1`), capital `H`/`G`, contextual footer. **No `r`/`d`/`i` action dispatch.** |
| E1\u2013E4 | Operator guide v1 \u2014 install + recover only (Sections 0-3 + 7 + 9). Explicit "do not commit" list. **Per-vendor playbooks + OAuth deep-dive deferred.** |

**v0.4 picks up:** D action handlers (`r`/`d`/`i`/`G`), B4 Tailscale
cleanup with allow-listing, E5\u2013E9 (vendor playbooks + recovery
scenarios + OAuth deep-dive), MCP surface, `cluster upgrade`
mutation, `secrets rotate`, `nostos doctor`.

**v1.0 unchanged:** Stable CLI, vendored iPXE, Homebrew/container,
hardware test matrix, 6+ vendor playbooks, man pages.
