# nostos v0.3 — planner summary

**Change dir:** `openspec/changes/nostos-v03-dashboard-and-hygiene/`
**Predecessor:** `openspec/changes/nostos-v02-provisioners/` (shipped)
**Roadmap split (non-negotiable):** v0.3 = Streams A+B+C+D(MVP)+E.
v0.4 = MCP + dashboard guided-fix + `cluster upgrade` mutation +
`secrets rotate`. v1.0 = stable CLI, vendored iPXE, Homebrew tap.

## Five streams

- **A. Bug fixes (v0.2 follow-ups).** A1 per-provisioner
  `MaxWaitMaintenance()` (tpi=30 min, pxe=0); orchestrator picks
  `max(opts, prov)`. A2 image-cache TOFU: stream-hash to `*.part`,
  rename, then write digest record; re-hash on cache hit.
  A3 BMC pre-flight (TCP→TLS→authed HTTP) emits typed
  `ErrBMCUnreachable` / `ErrBMCAuth` / `ErrBMCVersion` instead of
  bubbling up `os error 6`. A4 regression test pins the v0.2
  `.yaml` filename fix for tpi nodes.
- **B. Hygiene.** B1 `taskfiles/turing.yml` recipes become
  deprecation-and-exit-1 (no silent wrapper — they bypass the
  secrets pipeline). B2 `taskfiles/talos.yml::apply` for tp1/tp4
  routes through `nostos node install`. B3 `kubectl delete node
  talos-76w-r75` documented in the recovery guide (one-shot, not
  automated). B4 new `nostos cluster cleanup [--dry-run|--apply]`
  prunes Tailscale devices offline >7 days; default is dry-run.
- **C. AI-friendly CLI (8 principles).** C1 `--output {text,json,
  ndjson}` everywhere. C2 `nostos schema [<command-path>]`
  reflection-built JSON. C3 `--fields=a,b,c` projection on
  list/show/dashboard --once. C4 `--dry-run` on every mutation,
  emits a typed `Plan`, exits 8. C5 structured errors `{error,
  code, message, details, hint}` + pinned exit-code catalogue
  (0,1,2,3,4,5,6,7,8,9,10,64). C6 input hardening (node-name
  regex, `--config` path lock, `op://` refs no query). C7
  `.submodules/nostos/AGENTS.md` documents invariants. **C8
  DEFERRED to v0.4** (MCP server).
- **D. `nostos dashboard` TUI (Bubble Tea v2).** Discovery
  (ARP+ICMP cap=32, mDNS, Talos maint probe, Tailscale, ArgoCD,
  BMC). Match by MAC > IP > Tailscale-100.x; buckets known/orphan/
  unknown. Hardcoded check registry (`CheckID` constants), tiers
  fast/medium/slow/very-slow. Diff with upstream cached in
  `~/.cache/nostos/upstream-versions.json` (24h TTL). Aggregate
  state ALL_GREEN/DEGRADED/BROKEN. Action contract: every key
  routes through `internal/cli/dispatch/` shared with the CLI; no
  shelling-out from the TUI. Headless mode `--once --output json`
  for cron, exit 0 by default (BROKEN ≠ exit nonzero unless
  `--exit-nonzero-on-broken`). Living-docs pane reads
  `nostos/docs/<vendor>-<model>.md`; v0.3 ships defaults for
  dell-optiplex-3080m, turing-rk1, generic-amd64, raspberry-pi-5.
  XDG path: `~/.config/nostos/dashboard.toml`.
- **E. Operator guide.** Single Markdown file at
  `docs/nostos-guide.md` (NOT in the submodule). Sections 0-9 from
  the brief; auto-generated reference table from `nostos schema`;
  every command copy-pasteable; every error has a "what it means"
  entry; "Why" boxes explain design choices.

## Key decisions (full rationale in design.md)

- **D1** Per-provisioner `MaxWaitMaintenance()` over a cluster-wide
  bump; PXE failure-fast preserved.
- **D2** Hybrid TOFU fix: stream-hash + record-after-rename + re-hash
  on cache hit. Operator-pinned digests remain the security boundary.
- **D3** BMC pre-flight as a separate function called BEFORE the
  `tpi` binary; errno-6 from in-flight flash is also wrapped.
- **D4** `dashboard.toml` lives in XDG, not in `nostos/state/`.
- **D5** Hardcoded check registry (`CheckID` constants); plugin
  surface deferred to v0.4 with a known seam.
- **D6** `nostos schema` is a subcommand, not a `--describe` flag
  (cobra parsing constraints + composability with `--output ndjson`).
- **D8** Action dispatch shares one seam with the CLI so the MCP
  server in v0.4 slots above it cleanly.
- **D11** `nostos cluster cleanup` defaults to dry-run; `--apply`
  required to mutate.
- **D12** Pinned exit-code catalogue (0-10, 64) shared by AGENTS.md
  and `nostos schema`.

## Files written by this planner

- `openspec/changes/nostos-v03-dashboard-and-hygiene/.openspec.yaml`
- `openspec/changes/nostos-v03-dashboard-and-hygiene/proposal.md`
- `openspec/changes/nostos-v03-dashboard-and-hygiene/design.md`
- `openspec/changes/nostos-v03-dashboard-and-hygiene/tasks.md`
- `openspec/changes/nostos-v03-dashboard-and-hygiene/specs/dashboard/spec.md`
- `openspec/changes/nostos-v03-dashboard-and-hygiene/specs/cli-machine-output/spec.md`
- `v0.3/summary.md` (this file)

## Five questions for reviewers

1. **critic:** Is the v0.3 scope still too big? D-Roadmap defers MCP,
   guided-fix, and `cluster upgrade` mutation, but Streams D + E are
   each non-trivial. Should we split D (dashboard MVP) and E (guide)
   into v0.3a / v0.3b releases or hold the line?
2. **security-reviewer:** D2 (TOFU race) leaves a tiny window — crash
   AFTER `os.Rename` but BEFORE the digest record write — where the
   next run re-hashes a freshly-renamed file. Is that good enough, or
   should we require operator-pinned digests (no TOFU fallback at all,
   even behind a build tag)? Also: D3 BMC pre-flight does
   `InsecureSkipVerify=true` — fine for an in-LAN BMC, but should we
   document a TLS-pin upgrade path?
3. **UX-reviewer (Stream D needs this specifically):** the action
   contract (D8) routes every keypress through the CLI dispatch seam.
   This makes TUI/CLI behavior identical, but it means `i` (identify)
   must invoke a real subcommand. Is "press a key, see a `Plan`,
   confirm to execute" the right UX, or do we want a faster path for
   read-only actions? Also: is the aggregate-state tri-color
   (GREEN/DEGRADED/BROKEN) sufficient, or do we need a fourth
   "stale data" state when probes time out?
4. **test-engineer:** §6.4 "schema completeness gate" runs in CI Tier
   1 and fails the build if any cobra leaf is missing a schema entry.
   This is a good forcing function but might block PRs that
   intentionally add experimental commands. Should we allow a
   `cobra.Command{Hidden: true, ExperimentalNoSchema: true}` escape
   hatch, or hold the line?
5. **all reviewers:** §7.7 (JSONL run log + `--resume`) — currently
   held for v0.4. Given the dashboard already needs an event-stream
   contract for `node install`, is there meaningful overlap that
   makes v0.3 the right home? My current call: keep it in v0.4 to
   bound v0.3 scope. Looking for a second opinion.
