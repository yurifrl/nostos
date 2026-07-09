# Test Review: nostos v0.3 — dashboard + hygiene

Lens: testability + verification. Builds on `review-tests.md` (v0.2).
Reviewed: `openspec/changes/nostos-v03-dashboard-and-hygiene/{proposal,
design,tasks}.md`, both spec files, `v03-{brief,summary,review-critic,
review-security}.md`, `internal/cluster/orchestrate.go`,
`internal/provisioner/tpi/image_cache.go`.

## 0. TL;DR

The v0.2 seams (`Commander`, `Clock`, `Secrets`, `provisionertest`
compliance, flock) carry ~80% of v0.3 coverage. New surface needs:
five fakes (`FakeBMC`, `FakeTailnet`, `FakeKubeAPI`, `FakeDiscovery`,
`FakeProvisioner` with scripted timing), one SIGKILL property
harness against `image_cache`, one `teatest` model harness, and one
acceptance gate (`dashboard --once`). Three forcing functions:

1. **Headless `nostos dashboard --once --output json` is the source
   of truth.** TUI is a renderer of the same `State` value.
2. **Subsequence + shape matchers, not byte-equal goldens.** v0.2
   review §6.3 rule applies harder to JSON + Lipgloss.
3. **`nostos schema --output json` is the test fixture.** Property
   tests iterate the schema to enumerate commands; drift caught
   structurally.

Critic's three blockers (concurrent-install state, `--dry-run`
posture, exit-code 10 collision) are also test blockers — until
fixed in spec, the corresponding tests pin the wrong contract.

## 1. A1 — `MaxWaitMaintenance` without flashing

Pure unit test in `internal/cluster/orchestrate_test.go`, table-
driven over `(optsDeadline, provMaxWait, flagOverride) →
wantEffective`: rows for `(20m, 0, —, 20m)`, `(0, 30m, —, 30m)`,
`(15m, 30m, —, 30m)`, `(15m, 30m, 5m, 5m)`.

`FakeProvisioner` (in `provisionertest/fake.go`, extending v0.2)
exposes `MaxWait` and `MaintenanceUpAfter` fields; its
`WaitMaintenance` selects on `clock.After(MaintenanceUpAfter)` vs
`ctx.Done()`. Assertion: capture `time.Duration` passed to
`context.WithTimeout` at `orchestrate.go:182` (today: literal
`opts.WaitMaintenanceDeadline`; v0.3 must wrap in `max(...)`).
Compliance suite (`tasks.md:1.1`) adds one row: `MaxWaitMaintenance()
>= 0` over both providers.

## 2. A2 — TOFU stream-hash SIGKILL torture

Today's `image_cache.go:122-131` writes the digest record before the
final `os.Rename` (line 133) — exactly the race. Property test in
`internal/provisioner/tpi/image_cache_kill_test.go` (build tag
`property`):

- Build helper binary `testdata/imagecache/cmd/sigkill_writer/` that
  writes N bytes into `*.part` then halts; parent `fork`s ≥100
  children with N drawn uniformly in `[0, fullSize+512)`, sends
  `syscall.Kill(pid, SIGKILL)`, then runs `Ensure` with `httptest`
  serving the canonical bytes.
- Allowed end states: `{nothing}`, `{*.part orphan}`, `{final raw,
  no digest record}`, `{final raw, digest record matching final}`.
- **Forbidden:** any digest record whose value ≠ sha256(final), any
  partial final file, any digest record pointing at a missing file.

Property statement: any prefix of expected bytes followed by SIGKILL
leaves the cache in one of four allowed states; never in
{partial+digest} or {expected, mismatched-digest}.

Real OS-level kill (not goroutine panic) matters — only that exposes
the kernel-buffered-write window. Use rapid for the byte count
draw. Separate property: 24h `*.part` GC must not delete a file
under an active `flock` — needs `flock.AcquirePart()` (critic §4.10).

```
go test -count=1   -tags=property -run TestImageCache_SIGKILL_Property ./...   # PR
go test -count=1000 -tags=property -run TestImageCache_SIGKILL_Property ./...  # nightly soak
```

testdata: `internal/provisioner/tpi/testdata/imagecache/{full.raw.xz,
full.raw.xz.sha256, cmd/sigkill_writer/main.go}`. Synthetic 8 KiB xz.

## 3. A3 — BMC pre-flight error class table

`bmc_preflight_test.go` over `httptest.Server` + closed-port stub:

| Server behavior                     | Expected typed err              | Code              |
|-------------------------------------|---------------------------------|-------------------|
| port closed                         | `ErrBMCUnreachable`             | `bmc_unreachable` |
| accept, never reply (deadline)      | `ErrBMCUnreachable`             | `bmc_unreachable` |
| TLS handshake aborts                | `ErrBMCUnreachable`             | `bmc_unreachable` |
| HTTP 401 / 403                      | `ErrBMCAuth`                    | `bmc_auth`        |
| HTTP 404 on `/api/bmc/info`         | `ErrBMCVersion` (cannot detect) | `bmc_version`     |
| HTTP 5xx                            | `ErrBMCUnreachable`             | `bmc_unreachable` |
| 200 + version `"2.0.0"` (too old)   | `ErrBMCVersion`                 | `bmc_version`     |
| 200 + version current               | `nil`                           | —                 |
| Wrapped errno-6 from in-flight flash| `ErrBMCUnreachable` w/ "during flash" | `bmc_unreachable` |

Each row asserts `errors.Is(err, sentinel)`, the `Code()` string,
and **not** the OS errno text. Errno-6 row uses `FakeCommander`
returning `&exec.ExitError` wrapping `syscall.ENXIO`. Validates
`tasks.md:1.5`–`1.7`. Whole suite < 1 s, no live BMC.

## 4. B1 — Taskfile deprecation acceptance

`task turing:flash` must `exit 1` and stderr must match the literal
regex `^deprecated: use 'task nostos:install NODE=<name>'$` (pinned
from `tasks.md:2.1`). Same regex for `download`, `install-talos`,
`get`. Structural CI grep gate: `! grep -RE 'tpi flash|talosctl
apply-config' taskfiles/turing.yml`. Wired as `task test:taskfiles`,
GH-hosted Tier 1.

## 5. C1/C5 — JSON output without goldens

Goldens drift on key ordering and free-form `details`. Two
properties + per-command shape rules, all driven from the schema:

- **P1 (validity):** for every leaf, `nostos <cmd> --output json
  [--dry-run if mutating]` produces stdout that `json.Unmarshal`
  accepts. Iterate `loadSchema(t).Commands`; no hand list.
- **P2 (stream separation):** the set of JSON payloads on stdout
  is disjoint from stderr lines for the same invocation. Catches
  the spec contradiction (critic §4.8: "stderr empty under success"
  vs "hints always to stderr"). Recommend amending spec so success-
  with-hint puts hint in JSON `hint` field on stdout, stderr
  empty. Until amended, the test fails noisily.

Shape rules per command: `MustHave` JSONPaths (e.g.
`$.aggregate_state`, `$.nodes[*].name`) and `Forbidden` paths
(`$..password`, `$..tailscale_authkey`). `Forbidden` doubles as a
secret-leak gate (security review concern).

## 6. C2 — Schema round-trip

Three properties in `internal/cli/schema/exhaustive_test.go`:

- **forward:** every cobra leaf (walked via `walkLeaves(NewRoot())`)
  has a schema entry.
- **reverse:** every schema entry maps to a real cobra leaf.
- **per-flag:** `pflag.VisitAll` ⊆ schema flags for that leaf.

Plus the **stale-enum** drift gate (critic §3.5): for every
`enum`-typed flag in the schema, each listed value must NOT be
rejected with `ExitValidation`; one off-list value MUST be rejected
with `ExitValidation`. This is the missing half of `tasks.md:6.4`.
Tier 1.

## 7. C4 — Dry-run, ZERO subprocesses

Spec: `Commander` records ZERO invocations under `--dry-run`.
Critic §2.3 disputes whether `node install --dry-run` can honestly
preflight without invoking `tpi --version`. **Test the spec as
written** — when spec splits into "plan-only" vs "plan+preflight",
split the test. Generated from the schema for every `cmd.Mutating`:
run with `FakeCommander{}` and assert `len(fc.Invocations) == 0`.

**Property: dry-run output is JSON containing
`would_execute: [...]`, and re-running without `--dry-run` produces
an execution sequence that is a (sub)sequence of `would_execute`.**
Run `--dry-run`, capture `Plan.WouldExecute`. Run live with
`FakeCommander{ScriptedSuccess:true}`, capture `fc.Invocations`.
Assert subsequence equality. Live MUST NOT invoke anything the
plan didn't list.

**Exit-code 8 (critic §4.2):** `--dry-run && echo ok` is broken by
spec. Test should pin and document the chosen contract; recommend
exit 0 + JSON `"status":"preview"`. Either way, fail noisily on
contract flip.

## 8. C6 — Input hardening fuzz

Four fuzz targets, bidirectional (accepted ⇔ regex-matched):

- `FuzzNodeName` over `^[a-z0-9][a-z0-9-]{0,62}$`.
- `FuzzOpRef` — no `?`, `#`, `..` in `op://` URIs.
- `FuzzConfigPath` — reject paths resolving outside `{repo,
  $HOME}` AFTER symlink resolution (critic §5.8).
- `FuzzFieldMask` — no control chars, no JSON metacharacters.

5 s budget per target (`tasks.md:3.7`):

```
go test -fuzz=FuzzNodeName -fuzztime=5s ./.submodules/nostos/internal/config/
# similarly: FuzzOpRef, FuzzConfigPath, FuzzFieldMask
```

Corpus seeds at `testdata/fuzz/FuzzNodeName/`: empty, `\x00`,
`tp1\n`, `../`, Cyrillic `tр1`, 63/64-char boundary.

## 9. D — Dashboard MVP without a TTY

Bubble Tea v2 supports `tea.WithoutRenderer()`; drive `Update(msg)`
directly. No PTY. Two layers:

- **Pure model table tests** — keybindings: `m, _ = m.Update(key)`
  → assert `m.View()` substring. Cover `?` (help), `/` (filter),
  `q` (quit), `g` (guided), arrows. No goroutines, no clocks.
- **`teatest` for the event loop** (`charm.land/x/exp/teatest/v2`):
  `NewTestModel`, `Send(StateRefreshedMsg{...})`, `WaitFor` until
  `bytes.Contains(out, "BROKEN")`.

**Discovery layer** — `FakeDiscovery` returns scripted ARP / ICMP
/ mDNS / Tailscale / ArgoApps slices via `net.Interface` shim. Test
the match layer (MAC > IP > Tailscale-100.x) against hand-crafted
device sets. Critic §2.2 debounce/hysteresis: two consecutive
`Discover()` calls with a flickering device must emit
`status=transient`, not a flap.

**Health checks** — `httptest.Server` per check: `CheckK8sAPI`
→ `/healthz`, `CheckArgoSync` → kube list of `Application` CRs,
`CheckTailscale` → `/api/v2/tailnet/-/devices`, `CheckEtcdQuorum`
→ `FakeCommander` scripting `talosctl etcd members`. testdata:

```
internal/cli/dashboard/testdata/
  states/{all_green,degraded,broken,transitioning}.json
  k8s/{healthz_ok,healthz_503,nodes_ready,nodes_one_notready,
       apps_synced,apps_oneoutofsync}.json
  tailscale/{devices_4online,devices_offline_8d}.json
  bmc/{info_current,info_old}.json
```

**Action dispatch contract** (`tasks.md:4.7`): same `FakeCommander`
script must drive `dispatch.NodeReinstall("tp1")` and the dashboard
`r` action and produce equal `Plan`, equal `Error`, equal
`fc.Invocations`.

## 10. D — `--once --output json` as source of truth

Pour test budget here. The TUI is a renderer of this `State`.

1. **State construction** — fake probes feed `BuildState`. Shape
   rules: `$.aggregate_state ∈ {ALL_GREEN, DEGRADED, BROKEN,
   TRANSITIONING}` (TRANSITIONING required per critic §2.1);
   `$.nodes[*].name` matches the node-name regex; `$.checks[*].id`
   ∈ `CheckID` enum; `$.generated_at` parses RFC 3339.
2. **Aggregate-state derivation** — pure function table: all OK +
   0 orphans → ALL_GREEN; one warn → DEGRADED; one error →
   BROKEN; any orphan → DEGRADED; any node under active mutation
   → TRANSITIONING (pending spec fix; until then test pins wrong
   contract — block merge).
3. **Renderer determinism** —
   `lipgloss.SetColorProfile(termenv.Ascii)`, render twice, assert
   equality + substring presence (`"BROKEN"`, `"tp1"`); no byte
   golden.
4. **`--exit-nonzero-on-broken`** — exit 10 collides with "network
   unreachable" (critic §4.1). Pick a different exit (recommend
   11) before locking the test.
5. **`--fields` projection** — `--fields=aggregate_state` returns
   exactly that key, exit 0; `--fields=__bogus__` exits 2.

## 11. D — Living docs render stability

For each shipped playbook: read MD, render twice via
`dashboard.RenderMarkdown`, assert `v1 == v2` (determinism —
Lipgloss has had map-iter style ordering bugs); assert source
contains `## Hardware`, `## BIOS`, `## Recovery` (D9 headings); and
rendered output preserves them. Critic §3.7 says ship two
playbooks not four; renderer test needs N≥2 fixtures regardless of
which the operator owns.

## 12. E — Operator guide validation

`scripts/check-guide.sh` (Tier 1) does three things:
`markdown-link-check -q docs/nostos-guide.md`; extract every fenced
` ```bash ` block and run `bash -n` on it (syntax-only, no cluster);
`grep -oE 'nostos [a-z][a-z -]+' | sort -u | while read cmd; do
./.bin/nostos schema "${cmd#nostos }" --output json >/dev/null;
done` to assert every cited command exists in the schema. Replaces
`tasks.md:5.1`'s shellcheck-only gate (critic §7).

## 13. F — v0.3 acceptance gate (single command)

Post-fresh-install, in a clean operator env:

```bash
nostos dashboard --once --output json | jq -e '.aggregate_state == "ALL_GREEN"'
```

Plus the v0.2 test suite green. CI job `v0.3-acceptance`:

1. Build `./.bin/nostos`.
2. Bring up kind + fake Tailscale (`httptest`) + `FakeCommander`-
   mocked `talosctl`/`tpi`.
3. `nostos dashboard --once --output json --fields=aggregate_state`
   → assert `ALL_GREEN`.
4. Re-run with `--fields=nodes.name,nodes.status` → each configured
   node present + `status="Ready"`.

## 14. CI feasibility

| Test class                       | GH-hosted | Self-hosted KVM | Real HW |
|----------------------------------|-----------|-----------------|---------|
| `provisionertest` compliance     | ✓         | ✓               | —       |
| `image_cache` SIGKILL property   | ✓         | ✓               | —       |
| BMC pre-flight table             | ✓         | ✓               | —       |
| Schema round-trip + enum drift   | ✓         | ✓               | —       |
| Fuzz (5 s budget × 4 targets)    | ✓         | ✓               | —       |
| Dashboard model (teatest)        | ✓         | ✓               | —       |
| Headless `--once` vs fakes       | ✓         | ✓               | —       |
| Taskfile deprecation             | ✓         | ✓               | —       |
| Guide validation                 | ✓         | ✓               | —       |
| QEMU PXE end-to-end (v0.2)       | ✗         | ✓ nightly       | —       |
| Real `tpi flash` to RK1          | ✗         | ✗               | manual  |
| Real `--once` ALL_GREEN          | ✗         | ✓ if reachable  | ✓       |

Tier 1 budget ≤ 3 min (matches v0.2). v0.3 additions add ~30 s to
Tier 1 + ~10 s for SIGKILL property at 100 iterations; nightly
soak at 1000 iterations runs in Tier 2. **Everything testable in
Tier 1 except real-hardware paths** — that is the dividend of
treating `--once` as the contract.

## 15. Risks tests cannot catch

- **Critic §2.1 (concurrent install state) until spec adds
  TRANSITIONING.** Tests will pin the wrong aggregate-state
  contract. *Block code-merge until spec amended.*
- Real BMC firmware variation — hardware-only.
- mDNS/zeroconf raw-socket privileges (design Q5) — needs concrete
  fallback path or `t.Skip` with reason, never silent pass.
- Snapshot file forward-compat (critic §5.4) — add a `version`
  field now, even though forward compat is not yet a concern.

## 16. Recommended additions to tasks.md

- **6.11** Headless dashboard JSON shape rules (replace byte
  goldens, §5).
- **6.12** Schema enum/validation negative tests (§6).
- **6.13** Dispatch CLI ≡ TUI equivalence (§9).
- **6.14** Guide validation in CI Tier 1 (§12).
- **6.15** Aggregate-state derivation table (incl.
  TRANSITIONING once spec lands).
- **6.16** Fuzz corpus committed under `testdata/fuzz/` (§8).

## 17. Bottom line

v0.3 is testable on a laptop in <1 minute for everything except
PXE/TPI real-hardware paths, **provided** three spec items land
first (critic §9): TRANSITIONING aggregate state, `--dry-run`
posture, exit-code 10 collision. The headless `--once` mode is the
right surface to pour test budget into; the TUI is a renderer of
the same data and gets a thinner snapshot suite. v0.2 seams +
`FakeProvisioner` with scripted timing + `httptest`-backed BMC /
k8s / Tailscale fakes cover ~90% of v0.3 acceptance signals
without touching bare metal.
