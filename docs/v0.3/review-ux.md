# nostos v0.3 — UX review (Stream D, dashboard TUI)

Reviewed:
- `openspec/changes/nostos-v03-dashboard-and-hygiene/proposal.md` (Stream D block)
- `openspec/changes/nostos-v03-dashboard-and-hygiene/design.md` (D4–D11)
- `openspec/changes/nostos-v03-dashboard-and-hygiene/tasks.md` (§4)
- `openspec/changes/nostos-v03-dashboard-and-hygiene/specs/dashboard/spec.md`
- `v0.3/brief.md` (Stream D)
- `v0.3/summary.md`
- `v0.3/review-critic.md`
- `/Users/yuri/.agents/skills/charm-stack/SKILL.md`

**North star (per `proposal.md` Stream D, lines ~135-138 and `v03-brief.md:74`):**
"an operator who has never touched this lab can run `nostos dashboard` and
be guided to ALL GREEN without reading any markdown."

The current spec describes a *status* dashboard with action keys bolted on.
The north-star user is not in scope — they don't know what `tp1` is, they
don't have a kubeconfig yet, and they cannot recognize "etcd quorum OK" as
either reassurance or alarm. Most of the gaps below stem from that gap.

---

## 1. Information architecture

The mock in `v03-brief.md:78-95` and the contract in
`specs/dashboard/spec.md` Requirement 1 are **operator-fluent**. Columns:
hostname, Tailscale IP, Ready, role, version, OS, arch — i.e.
`kubectl get nodes -o wide` reformatted. A first-timer reading
`100.96.13.49` does not know it is a Tailscale CGNAT address; "Ready"
and "controlplane" are undefined words.

For first-time recovery the right groupings are **what to do next**:

- A persistent **"next step" line** at the top, derived from the worst
  failed check + `DocsAnchor` (`design.md:D5`): e.g., "Next: `nostos
  secrets test tailscale` — Tailscale OAuth not configured."
- Two-column layout: **Inventory** (devices) and **Health** (checks).
  The `[ checks ]` panel in the brief mock is buried under the node
  list; for a fresh user it should be the primary panel.
- Drop version/arch columns from the default view; behind `enter`.

`design.md:D8`'s nine action keys are too many for a 30-second first
read. `proposal.md` Stream D Non-Goal "Not a guided fix dispatcher" and
`tasks.md:7.2` defer the one feature (`g`-mode) that would actually
carry the north-star user — the headline contradicts scope (also
`v03-review-critic.md §6.5`). Either drop the headline or pull a
minimal `g`-mode (one button per failed check, dispatched as a CLI
verb, no new logic) into v0.3.

---

## 2. Failure modes

### 2.1 Empty cluster — "I just bought hardware"

`specs/dashboard/spec.md` Req 1 makes aggregate ALL_GREEN/DEGRADED/
BROKEN. With zero configured nodes and zero unknowns, every check
returns "n/a" and aggregate is implicitly ALL_GREEN. A first-timer
stares at a green bar and an empty list with no path forward. **This
is the showstopper for the north-star user.**

Fix: a fourth aggregate `UNCONFIGURED` when `len(config.nodes) == 0`,
body replaced by a four-step checklist: (1) `nostos init`,
(2) `nostos secrets test tailscale`, (3) plug a node in, (4) press
`n` on the discovered MAC.

### 2.2 Partially broken cluster

`specs/dashboard/spec.md` Req 1 Scenario 2: "BROKEN when any check
returns severity error or any node is unreachable." A single offline
worker reads as `BROKEN` — operationally correct but emotionally heavy
for a user who just unplugged a node intentionally. Show **what still
works** ("k8s API: ✓, etcd quorum: ✓, 2/3 workers Ready") before the
red banner. See also `v03-review-critic.md §2.1` on missing
`TRANSITIONING` state.

### 2.3 Unauthorized operator (no kubeconfig)

`design.md` Q2 leaves this open. `tasks.md:4.5` does not specify
behavior when kubeconfig is missing or context is wrong; today the
kubectl-driven probes (`design.md:D7::argocd.go`) hang or crash.

For the north-star user this is the **most common entry state** —
they haven't yet run `task argo:apply` or whatever sets up kubeconfig.
Required:

- Probe kubeconfig in `Init()`; if missing/wrong context, demote every
  kubectl-dependent check to `severity=info` with text "kubeconfig
  not configured — press `s`." Keep ICMP/ARP/mDNS running.
- Top-bar pill: `kubeconfig: not configured` /
  `kubeconfig: <context>`. Mirrors the kubectl-context guard in
  `CLAUDE.md`'s preamble; single most useful piece of context info
  for a first-timer; missing.

---

## 3. Affordance clarity

`design.md:D8` action keys: `i n h r d s u g / ?`.

- **Discoverability** of `i`/`n`/`r` is poor without `?`. The brief's
  mock (`v03-brief.md:94`) prints a footer; the spec does not require
  it. **Spec it:** always-visible footer, contextual to the selected
  row's bucket (known/orphan/unknown).
- **`r` (reinstall) wipes a disk.** `design.md:D8` says it produces a
  `Plan` first; right. But the spec must pin a two-keystroke confirm
  (`r`, then `Y` over the rendered Plan). `specs/dashboard/spec.md`
  Req 4 mentions "Plan ... shown ... before any side effect" without
  pinning keystroke discipline — first-timers will hit `r` exploring.
- **`d` is overloaded** (`v03-review-critic.md §4.4`): brief says
  k8s-zombie + Tailscale-stale, `design.md:D8` says cluster cleanup
  only. "Delete" should mean "delete the selected thing." Bind `d` to
  single-target delete; move multi-row sweep to a `:cleanup` palette.
- **Confirm ratio:** 3/9 mutating (`n`/`r`/`d`) is right. Read-only
  keys (especially `i`, §10) must NOT require Plan-confirm — that's
  too much friction for visible-only operations.

---

## 4. Cognitive load

- **Panels:** 3 visible + `s`/`?` modals = 5. OK.
- **Keys:** 9 actions + arrows + enter + `q` ≈ 13. OK for power users;
  contextual footer (§3) carries first-timers.
- **Help screen:** `tasks.md:4.1` derives `?` from `nostos schema`.
  Right SSoT instinct, but schema describes the *CLI*, not the TUI
  keys. Extend `schema/annotations.go` (`design.md:D6`) with `tui_key`
  + `tui_context`. Otherwise `?` dumps every CLI flag — opposite of
  helpful.
- **12-check registry** (`design.md:D5`): 4 × 5 per-node + 3 cluster +
  N apps ≈ 30+ rows on a 4-node cluster. Viewport must
  collapse-by-default; not in spec.

---

## 5. "Documentation in form of a program" — `s` panel

`design.md:D9` + `tasks.md:4.10-4.12`: `s` opens
`nostos/docs/<vendor>-<model>.md` in a Lipgloss viewport.

- **Vendor/model not in v0.2 config schema** (also
  `v03-review-critic.md §5.12`). On an unknown row, `<vendor>` is
  unknown — `s` should open a "supported hardware" index, not crash
  to "no playbook."
- **Offline links:** Lipgloss v2 supports OSC 8 hyperlinks (charm
  SKILL Hyperlinks); use them AND print bare URLs as fallback.
- **Fresh shell:** `tasks.md:4.11` says "ship default playbooks" but
  doesn't say *embedded*. For docs-as-program offline, mandate
  `embed.FS` in spec.
- **Edit-from-dashboard missing.** `nostos docs edit` (4.12) opens
  `$EDITOR` from the CLI but there's no key from inside the `s`
  viewer. Add `e` to drop into editor; round-tripping through a
  separate shell breaks the "single pane" promise.

---

## 6. Onboarding flow — 30 seconds

What a zero-context user understands from the brief's mock
(`v03-brief.md:78-95`) in 30 seconds:

- "There's a cluster called `talos-default`." (✓)
- "4/4 reachable" sounds good. (✓)
- A list of names with addresses and the word "Ready" repeated. (?)
- Some checks with checkmarks and one warning. (✓)
- A row of bracketed letters at the bottom. (?)

What's still confusing:

- Why am I here? What is the failure I'm fixing? Nothing on screen
  says.
- What does `controlplane`/`worker` mean to me?
- The `?` device row is there but says "press n to name" — what is
  "name" supposed to do? What's a node name? Why does it want one?
- "Talos v1.10.3 is 2 minor versions behind" — am I supposed to act?

**Concrete suggestion:** the top bar should carry an imperative for
the first-time user, not a status. State machine:

| Cluster state          | Top-bar text                                          |
|------------------------|-------------------------------------------------------|
| no nodes configured    | "Welcome. Press `n` on a discovered device to begin." |
| nodes configured, none reachable | "Cluster offline. Press `s` for the recovery guide." |
| degraded               | "1 node down — press `r` on tp4 to reinstall, or `s` for help." |
| all green              | "All systems normal. Press `u` to check for upgrades." |

`specs/dashboard/spec.md` Requirement 1 currently mandates exactly the
three-token aggregate. It should mandate a one-line imperative also.

---

## 7. Color/symbol semantics

`v03-brief.md:103-110`: ✓ / ⚠ / ✗ / ?. None of the below is in
`specs/dashboard/spec.md`:

- **Colorblind safety:** green/red is the worst primary axis (~8% of
  men). Mandate shape-carries-meaning, color augments — `✓`+bold,
  `⚠`+italic, `✗`+reverse.
- **NO_COLOR / piped to log:** Bubble Tea v2 honors it automatically
  (charm SKILL); verify symbols degrade (`✓→OK`, `⚠→WARN`, `✗→FAIL`).
- **No-Unicode terminals:** Lipgloss v2 has `ASCIIBorder`. Honor a
  `--ascii` flag — PXE consoles, busybox, serial sessions are exactly
  the recovery contexts this dashboard claims to serve.
- **Background detection:** mandate `tea.RequestBackgroundColor`
  (charm SKILL); default palette is illegible on light-on-light
  terminals without it.

---

## 8. Data freshness display

The four-tier cadence (`design.md` Definitions) is invisible. Spec:

- Per-check grey caption: `(15s ago)` after the symbol.
- Top-bar `last sync 47s ago` + spinner when mid-fetch.
- `R` key (or remap `r`) for force-refresh — useful right after
  plugging a cable.

Without this, the cold-start snapshot served from
`~/.cache/nostos/dashboard-state.json` (`tasks.md:4.9`) is
indistinguishable from live data. Also covers
`v03-review-critic.md §5.13`.

---

## 9. Empty states

Three empty cases the spec ignores:

- **0 configured + 0 discovered:** §2.1. Empty body + misleading green
  bar.
- **0 unknowns:** suppress the "unknown" panel or print a "no
  unconfigured devices" reassurance.
- **All-green:** today's mock just fills with ticks. All-green is the
  *exit* state; the screen should congratulate and surface the only
  next-step: `u` (if upstream diff non-zero) or "press `q`." Without
  this, the user assumes something must still be wrong — they came
  here to fix things.

---

## 10. The `i`dentify problem

`design.md:D8`: `i` → `nostos node identify`, "UART echo or LED blink"
for tpi; PXE/x86 falls back to traffic-burst (`v03-brief.md:115-118`).

- **`tpi uart set ... echo IDENTIFY`** writes to a serial console no
  one is watching — invisible for "which of these four boxes is
  tp4?".
- **`tpi power reset`** drains workloads. Unacceptable as identify.
- **NIC packet-flood** (e.g. `arping -c 50 -i 0.1`) lights activity
  LEDs hard enough to spot a box. Realistic, visible, works on every
  platform with a NIC.
- **Redfish `ComputerSystem.IndicatorLED`** is the right primitive
  long-term (Dell-class); out of v0.3 scope but worth a seam comment.

**Suggestion:** redefine identify priority: Redfish IndicatorLED → NIC
packet-flood → UART echo (last resort) → no-op with stderr "no visible
identify available; plug a labelled cable." Document per-platform
coverage in vendor playbooks (`tasks.md:4.11`). Current spec is
hand-waving.

---

## 11. Hidden devices

`design.md:D4` + `specs/dashboard/spec.md` Req 2 S3: hidden MACs in
`~/.config/nostos/dashboard.toml::hidden_devices`, dashboard "does not
render a row."

- **No way to find a hidden device.** Only path is `vim` the toml —
  violates "single pane." Add `H` (shift-h) for show-hidden view; in
  that view, `h` un-hides.
- **No notes/timestamps.** After a year nobody remembers why
  `aa:bb:...` was hidden. Spec the schema as
  `hidden_devices = [{mac, note, added}]`.

---

## 12. Markdown rendering

`design.md:D9` + `tasks.md:4.10`: Lipgloss-styled rendering. Spec
missing a Markdown engine — Lipgloss `lipgloss/v2/table` is a builder,
not a renderer. Glamour is the obvious Charm answer but isn't pinned.
Without it:

- **Tables** — pipe-tables unreadable at narrow widths.
- **Code blocks** — playbooks are ~50% commands; walls of grey
  without syntax highlight.
- **Links** — must use OSC 8 + print bare URL fallback (§5).
- **Width reflow** — hard-coded 80-col tables look bad at 200 and
  break at 60. Glamour reflows.
- **Embedded images** — screenshots can't render; spec a fallback
  like `[screenshot: bios-boot-order.png]` + `nostos docs open` to
  launch externally.

Pin the renderer in spec.

---

## 13. Headless `--once --output json`

`specs/dashboard/spec.md` Headless: shape "is part of the public
contract," but:

- **No JSON Schema committed.** `tasks.md:6.6` is a golden test —
  pins shape, doesn't help a consumer validate. Ship
  `schemas/dashboard-snapshot.v1.json`, reference from `nostos
  schema` and `AGENTS.md`. Also forces the schema-versioning hole
  (`v03-review-critic.md §5.4`).
- **No example output committed.** Ship
  `testdata/dashboard-once.example.json` for all-green / degraded /
  broken. Same cost as the golden test.
- **`--exit-nonzero-on-broken` collides with code 10** (also
  `v03-review-critic.md §4.1`). Pick a fresh code or document.
- **Field-mask discoverability.** Unknown `--fields=` fails closed
  (`design.md` Definitions). Error must include the available field
  list, else the agent burns three round-trips guessing.

---

## 14. TUI vs web UI for newcomers

A web UI on `localhost:8080` would be friendlier for the north-star
user (clickable links, real images, mobile access from the rack).
TUI still wins for v0.3 because:

- Recovery happens in SSH/serial sessions where a browser can't run.
- Web UI drags in auth/CSRF/TLS/ports — explicit scope creep.
- The dispatch seam (`design.md:D8`) extends to a web UI later for
  free; TUI-first costs nothing.

**Recommendation:** add Q10 to `design.md` enumerating "web UI as a
fourth surface above dispatch seam, post-v1.0," mirroring the MCP
entry in D-Roadmap. Otherwise reviewers keep asking.

---

## 15. Concrete suggestions, ordered by user-visible impact

1. `EMPTY`/`UNCONFIGURED` aggregate + get-started checklist (§2.1, §6).
2. Top-bar imperative line ("Next: …") instead of static aggregate (§6).
3. Kubeconfig-missing as documented degraded mode, not crash (§2.3).
4. Always-visible contextual action footer in spec (§3).
5. Two-keystroke confirm + visible Plan for `r`/`d`; no Plan-confirm
   for `i` (§3, §10).
6. Real `identify` semantics: Redfish > NIC packet-flood > UART > no-op
   (§10).
7. `H` show-hidden toggle + notes/timestamps in `dashboard.toml` (§11).
8. Symbol-not-color, NO_COLOR, ASCII-mode, background-detection
   mandated (§7).
9. Per-check freshness caption + global `last sync` indicator (§8).
10. Glamour (or equivalent) for markdown; `embed.FS` for playbooks
    (§5, §12).
11. JSON Schema + committed example for `--once --output json` (§13).
12. Help screen keymap-driven, not raw schema dump (§4).
13. Open-question entry for web UI as future surface (§14).

Items 1, 2, 3 are the difference between a dashboard that serves the
operator who already knows the lab and one that serves the person the
proposal claims to serve.
