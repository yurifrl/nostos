# nostos docs

Design, version, and pitch documentation for the nostos tool. These are the
working materials that fed (and feed) the formal specs in the parent repo's
`openspec/changes/nostos-v0{1,2,3}-*/` directories.

## Layout

```
docs/
├── README.md              ← this index
├── architecture.md        ← v1 architecture plan ("One CLI, every Talos node")
├── nostos-pitch.html      ← one-page pitch: bring bare metal home to the cluster
├── nostos-sim.html        ← live TUI dashboard simulator (HTML)
├── v0.2/                  ← Provisioner interface + tpi provider
└── v0.3/                  ← Dashboard TUI, AI-friendly CLI, hygiene, operator guide
└── design/                ← standalone design drafts (cross-cutting)
```

## Versions

### v0.2 — Provisioners (`docs/v0.2/`)

Extends nostos from x86-PXE-only to every Talos node via a `Provisioner`
interface (`Preflight / Prepare / Boot / WaitMaintenance / Cleanup`). First
new boot method: `tpi` (Turing Pi RK1 BMC flash). Closes the RK1 reset gap.

- `brief.md` — the planner brief (goals, scope, deliverables)
- `nostos-v02.html` — rendered architecture + session log
- `review-{analyst,critic,security,tests}.md` — four independent review lenses
- `synthesis.md` — post-review refinement

Formal spec: parent repo `openspec/changes/nostos-v02-provisioners/`.

### v0.3 — Dashboard, AI-CLI, hygiene (`docs/v0.3/`)

Closes the v0.2 bug list, hardens the CLI for AI/headless use (`--output json`,
`schema`, field masks, structured errors), ships a Bubble Tea v2 dashboard that
doubles as live documentation, and writes the canonical PXE+TPI operator guide.

- `brief.md` — planner brief (5 streams A–E)
- `nostos-v03.html` — rendered architecture
- `summary.md` — one-page planner summary
- `review-{critic,security,tests,ux}.md` — four review lenses (UX for Stream D)
- `synthesis.md` — post-review refinement

Formal spec: parent repo `openspec/changes/nostos-v03-dashboard-and-hygiene/`.

### Roadmap (non-negotiable split)

| Version | Scope |
|---------|-------|
| v0.2 | Provisioner interface + tpi provider |
| v0.3 | Bugs, hygiene, AI-friendly CLI, dashboard MVP, operator guide |
| v0.4 | MCP server surface; advanced dashboard actions; `cluster upgrade`; `secrets rotate` |
| v1.0 | Stable CLI, man pages, Homebrew tap, hardware test matrix |

## Design drafts (`docs/design/`)

Standalone, cross-cutting design thinking — not tied to a single version slice.
Several already landed as formal OpenSpec changes (see "Related" notes in each).

| File | Topic |
|------|-------|
| `cluster-bootstrap-controller.md` | In-cluster self-healing bootstrap controller (`nostos-bootstrap`) |
| `dashboard-feature-inventory.md` | Record of the deleted `nostos dashboard` (for rebuild) |
| `osimage-seam.md` | Make "which OS" a first-class pluggable axis (`osimage` registry) |
| `productization-plan.md` | Strip lab specifics; "smart" config |
| `pxe-generic-iso-proxmox.md` | Generic ISO netboot (Proxmox-aware), YAML-native |
| `pxe-reliable-ai-friendly.md` | PXE reliability, transparency, diagnostic hygiene |
| `render-injection-plan.md` | `nostos render` → emit bootstrap inline manifests |

## Provenance

These docs were bundled here from the parent repo's scratch space
(`.agents/tmp/nostos-v1/`, `.agents/drafts/`) so the tool's design history
lives with the tool's source rather than in the consumer repo's agent scratch
dir. The formal, tracked specs remain in the parent repo under
`openspec/changes/`.
