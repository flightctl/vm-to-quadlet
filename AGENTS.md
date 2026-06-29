# vm-to-quadlet — AI Agent Guide

This document orients AI agents (and new contributors) to the project layout,
module boundaries, and where to look when changing behaviour.

## Repository layout

```
vm-to-quadlet/
├── cmd/vm-to-quadlet/main.go    CLI entry-point (cobra); parses flags, calls
│                                run() → convertVM() → writeFiles()
├── pkg/kubevirt/
│   └── kubevirt.go              NewTemplateService() — KubeVirt TemplateService
│                                setup boilerplate for standalone mode (step 4 setup)
├── pkg/standalone/
│   ├── standalone.go            Options and PreparedVM structs
│   ├── pre_render.go            PrepareForRendering() — step 3: KubeVirt defaults
│   │                            + standalone tweaks; produces PreparedVM
│   ├── post_render.go           AdaptForStandalone() — step 5: Pod mutations for
│   │                            standalone/Podman (proxies, devices, init containers…)
│   ├── post_convert.go          ApplyPostConvertFixups() — step 7: passt hook injection
│   └── passt-workaround-hook.sh Embedded shell hook injected when --passt-workarounds
├── pkg/quadlet/
│   ├── types.go                 Public types: UnitFile, Options …
│   ├── converter.go             Convert() — steps 6a+6: preConvertFixups then
│   │                            in-process kube quadlet conversion
│   └── quadlet_test.go          Integration tests for the quadlet converter
├── internal/third_party/        vendored packages from the podman fork (see below)
│   ├── kube/quadlet/            Pod → Quadlet conversion engine (+ convert_test.go)
│   ├── systemd/                 INI parser and Quadlet unit-file writer
│   ├── k8s.io/                  Podman-vendored Kubernetes Pod types
│   ├── specgenutilexternal/     mount-type parser
│   ├── storage/regexp/          lazy-compile regexp wrapper
│   └── util/cpu.go              CPU period/quota helpers
├── examples/                    Ready-to-use VirtualMachine YAML files
├── docs/reference.md            Detailed field-by-field conversion reference
├── Containerfile                Standard single-module multi-stage build
├── Makefile                     build / test / lint / image / push / clean
└── go.mod                       module github.com/flightctl/vm-to-quadlet
```

This is a **fully standalone module**.  It depends on `kubevirt.io/*` and
`k8s.io/*` directly — it does not import `kubevirt-vm-to-pod` or any other
sibling repository.

## Pipeline

The conversion runs as a linear pipeline. Each stage is a direct named call in
`convertVM()` in `cmd/vm-to-quadlet/main.go`. Package prefixes make the origin
of each call unambiguous:

```
Step 2  readVM()                              local — os.ReadFile + yaml.Unmarshal
Step 3  standalone.PrepareForRendering()      OUR code — KubeVirt defaults + fixups
Step 4  kubevirt.NewTemplateService()         KubeVirt setup (boilerplate only)
        templateSvc.RenderLaunchManifest()    KubeVirt API call — undecorated
Step 5  standalone.AdaptForStandalone()       OUR code — Pod mutations for Podman
Step 6  quadlet.Convert()                     vendored kube quadlet
Step 7  standalone.ApplyPostConvertFixups()   OUR code — passt hook injection
```

See [`docs/reference.md`](docs/reference.md) for the full mapping table.

### Relationship to `kubevirt-vm-to-pod`

Both repositories solve the same standalone-VM problem from the same KubeVirt
starting point but target different output formats:

| Repo | Input | Output |
|---|---|---|
| `kubevirt-vm-to-pod` | `VirtualMachine` YAML | `pod.yaml` (for `podman kube play`) |
| `vm-to-quadlet` (this repo) | `VirtualMachine` YAML | Quadlet `.container` / `.volume` unit files |

## Key design rules

- **No migration code** — breaking changes to the Pod spec shape do not include
  backwards-compat shims.
- **No fallbacks** — if a required input is absent (e.g. a network binding),
  the code returns an error rather than silently picking a default.
- **Guard checks over if-else** — early returns for the failure/skip path,
  happy path falls through.
- **No tests/docs unless asked** — `quadlet_test.go` and this file exist because
  they were explicitly requested.

## Vendored packages from the podman fork

The following packages are copied verbatim from the
[asafbennatan/podman `pod-quadlet-converter` branch](https://github.com/asafbennatan/podman/tree/pod-quadlet-converter)
(locally at `~/dev/podman`), a fork of
[github.com/containers/podman](https://github.com/containers/podman).  The fork
adds a `podman kube quadlet` subcommand that does not yet exist in upstream
Podman — there is an open effort to contribute it upstream, at which point these
vendored packages can be replaced with a normal module dependency and this
`internal/third_party/` tree can be deleted.

Until then, they are vendored directly into this module so no external binary or
module dependency is needed at runtime:

| Package path | Source in fork |
|---|---|
| `internal/third_party/kube/quadlet/` | `pkg/kube/quadlet/` |
| `internal/third_party/systemd/parser/` | `pkg/systemd/parser/` |
| `internal/third_party/systemd/quadlet/` | `pkg/systemd/quadlet/` |
| `internal/third_party/specgenutilexternal/` | `pkg/specgenutilexternal/` |
| `internal/third_party/k8s.io/` | `pkg/k8s.io/` |
| `internal/third_party/storage/regexp/` | `vendor/go.podman.io/storage/pkg/regexp/` |
| `internal/third_party/util/cpu.go` | extracted from `pkg/util/utils.go` |

**Do not edit these files without a strong reason.**  Any change must either:
1. be pushed upstream to the fork first and then re-copied here, or
2. be clearly marked as an intentional local divergence with a comment
   explaining why.

The only modifications made during vendoring are:
- Import path prefix rewrite: `go.podman.io/podman/v6` → `github.com/flightctl/vm-to-quadlet`
  (and `go.podman.io/storage` → `github.com/flightctl/vm-to-quadlet/internal/third_party/storage`)
- A comment at the top of each modified file documenting the rewrite
- `internal/third_party/util/cpu.go` is a minimal extract (only `CoresToPeriodAndQuota` and
  related helpers) because the full `utils.go` pulls in heavy libpod/storage
  dependencies

## Running tests

```bash
make test
```

## Building the container image

The `Containerfile` is a standard single-module build — run it from inside the
`vm-to-quadlet/` directory:

```bash
make image IMAGE=quay.io/yourorg/kubevirt-vm-to-quadlet TAG=latest
# or directly:
podman build -f Containerfile -t <image> .
```

## Where things live by concern

| Concern | File(s) |
|---|---|
| CLI flags / entry-point | `cmd/vm-to-quadlet/main.go` |
| Pipeline skeleton | `cmd/vm-to-quadlet/main.go` → `convertVM()` |
| KubeVirt TemplateService setup | `pkg/kubevirt/kubevirt.go` → `NewTemplateService()` |
| Pre-render fixups (step 3) | `pkg/standalone/pre_render.go` → `PrepareForRendering()` |
| Post-render fixups (step 5) | `pkg/standalone/post_render.go` → `AdaptForStandalone()` |
| Post-convert fixups (step 7) | `pkg/standalone/post_convert.go` → `ApplyPostConvertFixups()` |
| Passt workaround hook script | `pkg/standalone/passt-workaround-hook.sh` |
| Standalone options | `pkg/standalone/standalone.go` → `Options`, `PreparedVM` |
| Pod → Quadlet conversion (step 6) | `pkg/quadlet/converter.go` → `Convert()` |
| Quadlet conversion options | `pkg/quadlet/types.go` → `Options` |
| Volume → Quadlet mapping | `internal/third_party/kube/quadlet/volume.go` (vendored) |
| Container → Quadlet mapping | `internal/third_party/kube/quadlet/convert.go` (vendored) |
| INI unit-file parser | `internal/third_party/systemd/parser/` (vendored) |
