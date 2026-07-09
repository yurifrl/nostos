# Test Review: openspec/changes/nostos-v02-provisioners

Lens: testability and verification. Not a security or design critique
(see `review-security.md`, `review-critic.md`, `review-analyst.md`).

Files: proposal.md, design.md, tasks.md, both spec files, the three
sibling reviews, `internal/cluster/orchestrate.go`.

## 0. TL;DR

Tasks.md proposes ~24 acceptance signals. About a third are string
matches against event kinds, a third are "manual evidence in PR", the
rest are unit-shaped but unspecified. The change is testable iff the
orchestrator is rewritten around three seams — `Commander` (exec),
`Secrets` (ref resolution), `Clock` (timing) — and the run-log is the
canonical, replayable source of truth. Without those seams the only
honest test is the destructive integration flash on real hardware.
## 1. Provisioner interface contract

Build a **compliance suite** in `internal/provisioner/provisionertest/`
exposing `RunComplianceSuite(t, factory func() Provisioner)`. Each
provider wires it once; per-provider files cover only provider-
specific behavior. Table-driven invariants:

| Invariant | Assertion |
|-----------|-----------|
| `Method()` stable, non-empty | two calls equal |
| Method matches registry key | `Registry.New(p.Method()) == p` |
| `Preflight` idempotent | two calls, same FakeCommander script, equal effects |
| Preflight failure typed | `errors.Is(err, ErrPreflight)` |
| Boot ctx cancellation | child ctx canceled → returns `ctx.Err()`, no orphan exec |
| `WaitMaintenance` deadline | FakeClock past deadline → `ErrTimeout` |
| Cleanup with fresh ctx | parent ctx canceled, Cleanup completes |
| Cleanup idempotent | two calls, no second-call error |
| `BMCKey(nv)` pure | property: same input → same key |
| Emits never contain resolved secret | scan emits for FakeSecrets values |

Registry tests: duplicate-`Register` panics with method in message;
`New("unknown")` returns typed `ErrNotRegistered`; blank-import wiring
tested in a small driver package to dodge cycles.

## 2. tpi without a real Turing Pi BMC

Two seams; recommend (A) for unit, (B) for one wiring smoke:

**(A) `Commander` interface in `tpi.Deps`:**

```go
type Commander interface {
    Run(ctx context.Context, name string, args, env []string,
        stdout, stderr io.Writer) error
}
```

`FakeCommander` records every `(name, args, env, stdin)` and serves
scripted output/exit per key. Kills 80% of tpi unit-test pain:

- 3.5 (no password in argv): `regexp.QuoteMeta(secret)` does not match
  `cmd.Args`; `slices.Contains(cmd.Env, "TPI_PASSWORD="+secret)`.
- 3.4 (4 GiB cache): inject `Statfs` helper, do not shell `df`.
- 3.8 (200ms throttle): scripted stdout at 1ms tick × 30s + FakeClock,
  count emits ≤ 151. Use `synctest` (Go 1.24) or pin a fake clock —
  do not depend on wall time.

**(B) Fake `tpi` binary on PATH** for one E2E CLI test:
`testdata/fake-tpi/main.go` reads `$NOSTOS_FAKE_TPI_SCRIPT`,
`t.Setenv("PATH", tmpBin+":"+...)`. Catches wiring bugs Commander
seam hides. Test container approach skipped — no public BMC emulator.

### tpi-specific scenarios (per spec/tpi-provisioning/spec.md)

| Scenario | Type | Seam |
|----------|------|------|
| Happy-path install | unit + emit subseq | Commander, Secrets, Clock |
| Image cache hit/miss | unit | httptest + tmp dir |
| Bad checksum refused | unit | httptest serves wrong bytes |
| Decompression | unit | tiny `.xz` fixture |
| Power-off → flash → power-on order | unit | Commander recording |
| Resolved password absent argv, present env | unit | Commander recording |
| Maintenance probe times out | unit | FakeClock + dial-EAGAIN |
| Cleanup on Boot error | unit | Boot returns err → assert power-off |
| Cleanup with parent ctx canceled | unit | Cleanup ctx fresh per spec |
| `(host, slot)` collision rejected | validator unit | yaml fixture |
| `tpi --version` too old | Preflight unit | scripted "0.4.0" → ErrPreflight |

## 3. PXE / x86 integration harness

QEMU/KVM, recommended for nightly self-hosted runs.

```
br-nostos-test (10.99.0.1/24, host-owned, no DHCP)
  ├── nostos (dnsmasq via sudo or rootless slirp)
  └── qemu-system-x86_64 -netdev bridge,br=br-nostos-test \
        -boot n -m 2G -nographic -nic mac=02:00:00:00:00:01
```

Artifacts: pinned Talos amd64 factory image (~80 MiB) under
`testdata/integration/`, downloaded by `TestMain` from a
SHA-pinned URL into `~/.cache/nostos-test/`; pre-rendered fake
`nostos/config.yaml` whose MAC matches the qemu nic.

Runtime: cold first run ~3 min (image fetch), warm 45-90 s per test
(PXE chain → Talos boot → maintenance → ApplyConfigInsecure → apid).
Tag `//go:build integration && pxe`. Self-hosted nightly only —
GitHub-hosted has no nested KVM, no privileged bridges; TCG fallback
is 4-6 min per test and flaky.

Acceptance signals (replacing brittle goldens):

- HTTP request log: exactly one each of `boot.ipxe`, kernel,
  initramfs, config.
- `talosctl --insecure version` parses before WaitMaintenance
  deadline (replaces TCP-50000 probe; see analyst §1.2 / critic §4.7).
- Last run-log line is `kind=apid-up` (not the synthetic `ready`).
- dnsmasq pid gone after Stop; bridge has no leftover leases.

## 4. Run-log resumability (kill -9 mid-flight)

`tasks 1.5` ("N events → N lines") is too weak. Real contract: after
SIGKILL the file is parseable AND a re-run is idempotent.

```
TestRunLog_KillMidWrite (property, ≥100 iterations)
  child writes 100k events with random sleeps
  syscall.Kill(child, SIGKILL) at random offset
  parent reopens:
    every line valid JSON or zero bytes (truncated tail allowed)
    last full line's seq monotonic; line count ≤ events sent
```

Resume idempotency (plumbing now, even though `--resume` is 7.7):
fake provisioner with checkpoint after each phase; panic at
`phase=Boot`; re-run with `--resume <run-id>`. Assert: Preflight runs
again (idempotent by contract), Prepare skipped (sha256 unchanged),
no destructive op invoked twice — i.e. `count(Commander["tpi flash"])
≤ 1` unless `--reflash` set.

## 5. BMC contention / parallel installs

`tasks 4.4` + spec scenario "Two slots serialize":

```go
func TestBMCSemaphore_SerializesSameKey(t *testing.T) {
    sem := provisioner.NewBMCSemaphore()
    var order []string; var mu sync.Mutex
    run := func(n string) {
        rel := sem.Acquire("tpi:192.168.68.10"); defer rel()
        mu.Lock(); order = append(order, n+":enter"); mu.Unlock()
        time.Sleep(20 * time.Millisecond)
        mu.Lock(); order = append(order, n+":exit"); mu.Unlock()
    }
    // launch a,b,c concurrently; assertNoOverlap(order)
}
```

Different-keys variant (`tpi:host1` vs `tpi:host2`) MUST overlap; use
a `sync.Barrier`-style rendezvous, not sleeps. Cross-process
contention (security §9) needs `flock`, not the in-process semaphore;
out of v0.2 scope per tasks — skip with reason, never silent pass.

## 6. Backwards-compat (dell01 still installs)

Three layers, all required:

**6.1 Schema load** (`tasks 6.4`): pin the literal repo
`nostos/config.yaml` and `talos/nodes/dell01-192.168.68.100.yaml`;
load → no error; `node["dell01"].Boot` nil or `Method=pxe`.

**6.2 Render determinism**: pre/post-refactor render of dell01's
machineconfig must sha256-match modulo the schematic-version line.
Golden at `testdata/golden/dell01.machineconfig.yaml`, updatable only
via `-update` flag.

**6.3 Event sequence**: tasks 4.1's literal-list golden is brittle
(critic §10, analyst §4.2). Replace with **topological subsequence**:

```go
required := []EventKind{KindInfo, KindProgress, KindProgress,
  KindProgress, KindDownload, KindDownload, KindDownload,
  KindConfigFetched, KindNodeUp, KindApidUp, KindReady}
assertSubsequence(t, observed, required)  // not equality
// plus: assertNoEvent(t, observed, KindError)
```

## 7. Manual test matrix per provider

Hardware available (per CLAUDE.md, `talos/nodes/`):

| Provider | Hardware | Manual signal |
|----------|----------|---------------|
| pxe | dell01 (OptiPlex 3080M) | `task nostos:install NODE=dell01-test` reaches maintenance, joins etcd, `kubectl get node dell01` Ready |
| tpi | tp1, tp4 (Turing Pi RK1) | flash slots 1 & 4; `talosctl version -n 192.168.68.107` returns; node Ready |
| redfish | none in lab; borrowable iLO/iDRAC | n/a in v0.2 (deferred 7.1) |
| proxmox | pc01 hosts vm-pc01 | n/a in v0.2 (deferred 7.2) |
| usb | pc01 (NVIDIA, amd64) | dd test image, manual checklist only |
| rpi-imager | retired RPi4 in storage | deferred 7.4 |

Replace tasks "manual evidence in PR description" with structured
`.github/PULL_REQUEST_TEMPLATE/hardware.md`: hardware, slot, image
sha, run-id, attached log. Reviewers can grep.

## 8. Property-based testing (`pgregory.net/rapid`)

Targets that pay off:

- **Config validator**: random `Node` structs (biased to edge cases:
  empty strings, unicode, host+slot collisions). `Validate` is
  deterministic; YAML round-trip equals identity.
- **Machineconfig render determinism**: same `Config` + same secret
  table → byte-identical output across N runs. Catches map-iteration
  order bugs.
- **`BMCKey` purity**: depends only on `(host, port)`, not `Name`/
  `Arch`. Generate permutations, assert equivalence classes.
- **Run-log replay**: any `EventKind` sequence → JSONL → readback
  equal. With a fault-injecting `io.Writer`, "every flushed line
  round-trips" exactly.
- **Redaction**: ∀ string `s`, secret table `T`,
  `Scrub(s, T)` contains no `t ∈ T` as substring. Counter-examples
  surface overlapping-secret bugs (one secret is prefix of another).

Gate behind `-tags=property` if seed time matters.

## 9. CI feasibility

**Tier 1 — every PR, GitHub-hosted ubuntu-latest, < 3 min:**

```bash
go test ./.submodules/nostos/...
go test -race ./.submodules/nostos/...
go test -tags=property ./.submodules/nostos/...
go vet ./.submodules/nostos/... && golangci-lint run
```

Covers compliance suite, all fakes, in-process kill torture (goroutine
panic, not SIGKILL), validators, render goldens.

**Tier 2 — nightly, self-hosted with KVM:**

```bash
go test -tags='integration pxe' -timeout=20m \
    ./.submodules/nostos/internal/provisioner/pxe/...
```

Skipped with reason if `kvm-ok` fails — never silent pass.

**Tier 3 — manual / `run-hardware-tests` PR label:**

- `go test -tags='integration tpi' ./.../tpi/...` against real Turing
  Pi.
- Real dell01 reinstall.

## 10. Test-data management

```
.submodules/nostos/internal/provisioner/testdata/
  golden/
    dell01.machineconfig.yaml
    dell01-install.events.jsonl
    tp1.machineconfig.yaml
  fixtures/
    config-v01-dell01-only.yaml      // backwards-compat anchor
    config-v02-full.yaml
    config-invalid-collision.yaml    // tp1 & tp4 same (host,slot)
    config-inline-secret.yaml        // literal password → reject
  ipxe/boot.ipxe.expected, boot-arm64.ipxe.expected
  images/talos-test.raw.xz           // 1 KiB synthetic xz
  op/fake-op-responses.json          // {ref → resolved}
```

Rules: goldens via `-update` only; fixture secrets are sentinels
(`fake-bmc-password-do-not-rotate`) the redaction lint actively scans;
integration images sha256-verified from `manifest.json`; fake op
responder is a `Secrets` impl, not the real `op` binary.

## 11. Concrete `go test` invocations

```bash
# Inner loop (~10 s)
go test ./.submodules/nostos/internal/provisioner/...
# Race + props
go test -race -tags=property ./.submodules/nostos/...
# Compliance only
go test -run 'TestPXECompliance|TestTPICompliance' \
    ./.submodules/nostos/internal/provisioner/...
# Update goldens after intentional render change
go test -run TestDell01Render -update \
    ./.submodules/nostos/internal/provisioner/pxe/
# Kill torture, repeated
go test -count=10 -run TestRunLog_KillMidWrite \
    ./.submodules/nostos/internal/runlog/
# QEMU PXE (KVM required)
sudo go test -tags='integration pxe' -v -timeout=20m \
    ./.submodules/nostos/internal/provisioner/pxe/
# Real tpi (operator only)
NOSTOS_TPI_HOST=192.168.68.10 NOSTOS_TPI_SLOT=4 \
go test -tags='integration tpi' -v -timeout=15m \
    ./.submodules/nostos/internal/provisioner/tpi/
```

Wrap as `task nostos:test`, `:test:integration`, `:test:hardware`.

## 12. Acceptance signals — translating tasks.md "works"

| Task | Vague | Observable |
|------|-------|------------|
| 1.2 | panics on duplicates | `recover()` contains method name; `errors.Is(ErrNotRegistered)` for unknown |
| 1.5 | N events → N lines | + mode 0600, parent 0700, last `seq == N`, every line `json.Valid` |
| 1.6 | fails when planted secret leaks | 5-sentinel fixture; runtime Scrubber wraps emitter; lint scans `phase`, `message`, Commander stdout/stderr |
| 2.1 | loads without error | + dell01 YAML round-trip byte-equal modulo formatting |
| 2.2 | error cites field path | must contain `node[tp1].boot.tpi.host`-style path |
| 2.3 | shows BMC host, no password | stdout matches `host:`, NOT resolved password (FakeSecrets sentinel) |
| 2.4 | pins the guard | schema-level: non-`_ref` field with `op://`/`sops://`/high-entropy fails to unmarshal, typed error |
| 3.2 | download + skip-on-cached | httptest hit count == 1 across two `Prepare`s; sha256 matches manifest |
| 3.4 | mocked Preflight | exactly: `tpi --version` ×1, dial-tcp ×1, `Secrets` ×1/ref, Statfs ≥ 4 GiB |
| 3.5 | argv/env split | both halves; `TPI_PASSWORD` NOT propagated to `talosctl` env |
| 3.7 | Cleanup on error | Boot → `ErrBoot`; FakeCommander recorded `tpi power off`; Cleanup ctx deadline > parent |
| 3.8 | "fewer than ~150 emits" | pin ≤ 151 with FakeClock; drop the tilde |
| 4.1 | golden Event sequence | topological subsequence (§6.3), not byte-equal |
| 4.2 | phases in order | exact order `Preflight, Prepare, Boot, WaitMaintenance, ApplyConfigInsecure, WaitApid, [Bootstrap]`; Cleanup defers on success + each injected error |
| 4.3 | subprocess argv | exact argv; rendered config unlinked after success (security §3) |
| 4.4 | second blocks until first | §5 barrier-based |
| 4.5 | compares emits | per emit `Kind`, `Phase`, `Node`, `At` monotonic; secrets absent |
| 5.1 | calls Install once | count == 1, NodeView equals fixture |
| 5.2 | stderr includes deprecated | pin `^deprecated: nostos up; use 'nostos node install <name>'$` |
| 5.3 | parallel land in v0.3 | exit 2, stderr matches `^--parallel not implemented in v0\\.2` |
| 5.5 | exit code 2 | + stderr cites change id |
| 6.3 | manual evidence | structured PR template (§7) |
| 6.4 | pins regression | literal repo config fixture; CI fails any schema change without golden update |

## 13. Risks tests cannot catch (be honest)

BMC firmware variation across `tpi` versions — hardware-only.
Talos factory image schema changes — pinned fixtures hide drift; cover
via `nostos doctor` manifest diff (7.8). Network partition mid-flash
(critic §7) — chaos-Commander models, does not exercise. Cross-laptop
concurrent operator — needs flock; out of v0.2 scope; document as gap.

## 14. Recommended additions to tasks.md

**6.5** publish `internal/provisioner/provisionertest` compliance
suite; pxe and tpi pass with no skips. **6.6** `Commander` seam in
every provider; `grep -R exec.Command internal/provisioner/{pxe,tpi}/*_test.go`
returns zero. **6.7** SIGKILL torture, 100 iterations default, no
flakes over 1000 CI runs. Promote 6.3 to
`.github/PULL_REQUEST_TEMPLATE/hardware.md`. Replace 4.1 / 6.2
literal-sequence goldens with subsequence + "no `KindError`" check.

## 15. Bottom line

Land three seams in PR #1 — `Commander`, `Secrets`, `Clock` — and the
`provisionertest` compliance suite, before any provider migration.
With them, ~90% of spec scenarios are unit-testable on a laptop in
under 10 s; QEMU integration covers PXE end-to-end nightly; the
real-hardware tier shrinks to flash-an-RK1 / boot-a-Dell smoke.
Without them, "works" stays a string match and regressions ship.