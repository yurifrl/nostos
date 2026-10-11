# `nostos upgrade-k8s` + worker-upgrade fixes

Derived from the 2026-10-11 cluster upgrade session (talos-default: 1.13.3 → 1.14.2, k8s 1.32.1 → 1.37.1). Every item below was a real failure or manual step during that run.

## Phase A — fixes to existing `nostos upgrade`

### A1. Worker upgrades never reboot (P0)

**Observed:** every worker upgrade (`tp1`, `tp4`, `dell01`, later `rpi01` via talosctl) staged the new install (`installation of v1.14.2 complete`, installer exit 0), then talosctl aborted: its pre-reboot drain fetches kubeconfig **from the target node**; workers answer `Unimplemented: kubeconfig is only available on control plane nodes`. talosctl exits non-zero → nostos reports failure → **no reboot** → node keeps running the old version with the new install staged in the inactive slot.

**Fix** in `internal/upgrade/detect.go` `Upgrade()`: on non-zero exit, inspect combined output for `installation of <version> complete`. If present, run `talosctl reboot --nodes … --endpoints …` and treat the node as "staged → rebooting"; `WaitHealthy` already covers the rest. Only surface failure when the install itself didn't complete.

**Alternative considered:** pre-drain via the cluster endpoint (`cluster.endpoint`) with kubectl before invoking talosctl. Rejected for now: extra dependency on apiserver availability during the upgrade window; the output-detection path is self-contained.

### A2. `--all` aborts on one unreachable node

**Observed:** `nostos upgrade --all` (the default) failed version detection on `macarm01` (VM down) and refused to plan the other six nodes. Whole session fell back to per-node invocations, losing automatic workers-first ordering.

**Fix** in the planner (`internal/upgrade/plan.go` + `detect.go`): unreachable nodes become plan entries with status `skipped-unreachable`, listed in the final summary. Ordering sweep still workers-first, CP last. `--dry-run` shows them; `--yes` proceeds with the reachable set.

### A3. Endpoint fallback: `lan_ip` → `tailscale_ip`

**Observed:** `nostos upgrade rpi01` targeted `192.168.9.170` (offsite, LAN unreachable) while the node answered fine at its tailscale IP. `config.yaml` already carries both addresses per node.

**Fix:** node dial/execute helpers try configured addresses in order (`lan_ip`, `tailscale_ip`), remember the first that works for the remainder of the command. Applies to `upgrade`, `apply`, `status`.

### A4. Health gate: version is not health

**Observed:** after `tp4`'s upgrade its kubelet hung in SIGTERM (`Stopping`, health Fail) for >3 min; talos version already reported v1.14.2 and nostos's `WaitHealthy` declared success while the k8s node was `NotReady`. Required manual reboot.

**Fix** in `WaitHealthy` (or a post-step): after version matches, verify (a) `talosctl service kubelet` reports `Running/OK`, (b) for cluster members, the k8s Node object is `Ready`. Gate CP steps additionally on etcd health. Timeout → error naming which check failed.

### A5. `nostos status` version columns

**Observed:** `nostos status` printed `—` for every version, all session. Talos + kubelet versions per node would have replaced half the ad-hoc `talosctl version` probes.

**Fix:** populate the VERSION column from `talosctl version -n <ip>` (short timeout) and add a KUBELET column from the Node object (`kubectl get nodes`). Missing values stay `—` (unreachable), never fail the command.

## Phase B — new command: `nostos upgrade-k8s --to v1.37.1`

Nostos already owns every k8s version pin (kubelet in per-node templates; apiserver/controller-manager/scheduler/proxy in the CP template) but has no command to act on them. The whole k8s ladder in the reference session was manual: 4 × `talosctl upgrade-k8s` hops, kube-proxy DaemonSet floor un-sticking, per-node kubelet patches (JSON6902 broke on multi-document configs produced by `--with-docs/--with-examples`), and manual template re-pinning to remove drift.

### Flow

1. **Floor detection** — lowest version among kube-apiserver/controller-manager/scheduler/kube-proxy *pod specs* in `kube-system` (mirrors talosctl's `DetectLowestVersion`, which scans pod specs, not DaemonSets). If floor is held back by a lagging kube-proxy DS rollout: `kubectl set image ds/kube-proxy` → wait rollout → re-detect. Warn if any Node object's kubelet version < floor ("zombie nodes pin the ladder — run `nostos cluster cleanup` or delete stale Nodes").
2. **Hop planning** — validate consecutive +1-minor path floor → `--to`; refuse skip-minor up front (talosctl would anyway, but the error should name the hop list).
3. **Per hop** (workers are never >1 minor ahead of the apiserver):
   - `talosctl upgrade-k8s --to <hop> --upgrade-kubelet=false --manifests-no-prune` against the **CP endpoint** (not a worker). `--manifests-no-prune` is mandatory-by-default: the prune phase once attempted to delete Argo-owned `ClusterSecretStore/onepassword` and only the ESO webhook being down saved it. Provide `--allow-manifest-prune` escape hatch for completeness.
   - **Kubelets via nostos-native render→apply**: bump `ghcr.io/siderolabs/kubelet:v<hop>` in each reachable node's template → `nostos render <node>` → `nostos apply <node>`. No reboot needed (kubelet restarts on config apply). This deliberately avoids `talosctl patch`: multi-document machine configs reject JSON6902, and templates stay the single source of truth — no live/repo drift.
   - **Gate**: all reachable nodes `Ready` at the hop version (kubectl), kube-proxy pods all on the hop image. Timeout → abort with per-node state.
4. **End state** — templates + config pins sit at the target; `git diff` is the commit. Print the suggested commit message. Never auto-commit from within nostos.

### Flags

| Flag | Default | Meaning |
|---|---|---|
| `--to` | required | target k8s version |
| `--nodes` | reachable set from config | restrict kubelet bump subset |
| `--dry-run` | false | print hop list + which templates change per hop |
| `--yes` | false | required (restarts control plane + kubelets) |
| `--kubelet-timeout` | 5m | per-hop Node-Ready gate |
| `--allow-manifest-prune` | false | opt back into talosctl manifest pruning |

### Non-goals

- No etcd backup orchestration (talosctl already snapshots on upgrade-k8s; document it in `--help`).
- No addon (CNI/CSEM/ArgoCD) version management — those are Argo/Helm territory.
- No auto-commit to the parent repo (repo rules: GitOps flow owns that).

## Sequencing

1. A1 (worker reboot) — smallest, unblocks the core OS path.
2. A2 + A3 (planner resilience) — makes `nostos upgrade --all` actually usable off-site.
3. A4 + A5 (gates + visibility).
4. B (upgrade-k8s) — floor detection first, then hop loop, then template-sync step.

## Reference evidence (2026-10-11 session)

- Worker drain aborts: bg_1/bg_3/bg_6 outputs, `error creating Kubernetes client for drain … Unimplemented`, nostos rc=0 (also fix exit-code propagation: nostos exited 0 while the embedded talosctl failed — return non-zero when the flow degrades to manual intervention).
- `--all` abort: `Error: detect version for macarm01 (192.168.68.90) … exit status 11`.
- rpi01 tailscale reachability: LAN `192.168.9.170` dead, `100.102.238.43` answered apid.
- tp4 kubelet SIGTERM hang: service `Stopping` 2m35s+, node `NotReady`, talos version v1.14.2.
- kube-proxy floor: `unsupported upgrade path 1.32->1.34` persisted until lagging DS pods were rolled (floor scans pod specs).
- JSON6902 vs multi-doc: `JSON6902 patches are not supported for multi-document machine configuration` on all 5 nodes after first upgrade-k8s hop.
- Manifest prune near-miss: `ClusterSecretStore/onepassword delete failed … external-secrets-webhook … connection refused`.
- Deprecated config caught manually: `pod-security.admission.config.k8s.io/v1alpha1` → `v1` in CP template (candidate future lint: template validation against target version's deprecated fields).
