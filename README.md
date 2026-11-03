# Kube-Op-Lite

A lightweight Kubernetes **operator** written in Go that manages a custom resource
called `GpuWorkload`. Creating a `GpuWorkload` causes a controller to reconcile it
into real Kubernetes **Deployments** and **Services**, track its lifecycle through
defined phases, and enforce validation rules via **admission webhooks**.

It mirrors the patterns NVIDIA's GPU Operator uses — CRDs, controllers,
reconciliation loops, owner-reference lifecycle automation — in a self-contained,
locally-runnable form.

GPU requests are **real scheduler resources**: pods ask for `nvidia.com/gpu`, Kind nodes
advertise fake GPU capacity, and `priority` maps to PriorityClasses, so the stock
kube-scheduler bin-packs GPUs and **preempts** low-priority work when they run out.
No GPU hardware needed.

[![Open in GitHub Codespaces](https://github.com/codespaces/badge.svg)](https://codespaces.new/Arsh624/kube-op-lite)
&nbsp;Opens a 4-core cloud dev box with Go, Docker, Kind, kubectl and Helm. Then run
`make demo && make preemption-demo`.

> Built with [Kubebuilder](https://book.kubebuilder.io/) / controller-runtime,
> packaged with Helm, and validated on a local [Kind](https://kind.sigs.k8s.io/) cluster.

---

## Architecture

```
            kubectl apply gpuworkload.yaml
                          |
                          v
                  +---------------+        admission         +------------------+
   You  ───────►  |  API Server   | ───────────────────────►|     Webhooks     |
                  +---------------+                          |  mutate: default |
                          ^                                  |  validate: reject|
                          | reads status back                +------------------+
                          |                                           |
                          |                                    accepted CR
                          |                                           v
                  +-------------------------------------------------------+
                  |   GpuWorkload controller — the reconcile loop         |
                  |   1. read desired (spec)                              |
                  |   2. read actual (cluster)                            |
                  |   3. make them match  → create/update children       |
                  |   4. write status.phase, requeue until converged     |
                  +-------------------------------------------------------+
                          |  owns (cascade delete via owner references)
              +-----------+-----------+
              v                       v
        +-----------+           +-----------+
        | Deployment|           |  Service  |
        | (the pods)|           | (network) |
        +-----------+           +-----------+
```

**Core principle:** desired-state vs actual-state, loop until matched.

---

## The `GpuWorkload` custom resource

```yaml
apiVersion: gpu.kube-op-lite.io/v1alpha1
kind: GpuWorkload
metadata:
  name: gpuworkload-sample
spec:
  replicas: 3          # desired pod count        (min 1)
  image: nginx:1.27    # container image
  gpuCount: 2          # nvidia.com/gpu per pod    (0–8)
  priority: high       # low | medium | high       (PriorityClass, enables preemption)
```

| Field | Type | Rules |
|-------|------|-------|
| `spec.replicas`  | int  | required, ≥ 1 |
| `spec.image`     | string | required, non-empty |
| `spec.gpuCount`  | int  | 0–8 (rejected if > 8) |
| `spec.priority`  | enum | `low` / `medium` / `high`, defaults to `medium` |

**Status** reports `phase` (`Pending → Provisioning → Running → Draining → Terminated`),
`availableReplicas`, `conditions[]` (`Available`, `GpuUnschedulable`), and `lastReconcileTime`.
When pods can't be placed, `GpuUnschedulable=True` carries the scheduler's reason,
e.g. `0/1 nodes are available: 1 Insufficient nvidia.com/gpu`.

---

## GPU scheduling and preemption

| Priority | PriorityClass | Value |
|----------|---------------|-------|
| `low`    | `kube-op-lite-low`    | 1000 |
| `medium` | `kube-op-lite-medium` | 10000 |
| `high`   | `kube-op-lite-high`   | 100000 |

The controller creates these PriorityClasses on demand. `make kind-gpus` patches each
Kind node's status with `nvidia.com/gpu: 4`, the same extended resource the NVIDIA
device plugin registers, so the scheduler does the real accounting.

`make preemption-demo` on a 1-node, 4-GPU cluster:

1. `training-low` (2 pods x 2 GPUs, low) fills the node and goes `Running`.
2. `inference-high` (1 pod x 2 GPUs, high) arrives. No GPUs are free, so the scheduler
   evicts a low-priority pod and `inference-high` goes `Running`.
3. `training-low` drops to `Pending` with `GpuUnschedulable=True` until GPUs free up.

## Metrics

Exposed on the manager's `/metrics` endpoint alongside controller-runtime's built-ins:

| Metric | Type | Meaning |
|--------|------|---------|
| `kube_op_lite_gpus_allocated{namespace,name,priority}` | gauge | GPUs held by available pods |
| `kube_op_lite_time_to_running_seconds` | histogram | creation → first `Running` |
| `kube_op_lite_unschedulable_total{priority}` | counter | transitions into `GpuUnschedulable` |

---

## Repository layout

```
api/v1alpha1/
  gpuworkload_types.go        # CRD spec/status definitions
  gpuworkload_webhook.go      # validating + defaulting webhooks
  zz_generated.deepcopy.go    # generated DeepCopy methods
internal/controller/
  gpuworkload_controller.go   # the reconcile loop (the heart)
  *_test.go                   # envtest unit tests
cmd/main.go                   # manager entrypoint
config/
  crd/bases/                  # generated CRD manifest
  samples/                    # example GpuWorkload
charts/kube-op-lite/          # Helm chart (CRD, RBAC, operator Deployment, webhooks)
Dockerfile  Makefile  PROJECT
```

---

## Prerequisites (one-time setup)

This project uses Kubebuilder, which **does not run on native Windows** — use
**WSL2**, Linux, or macOS. On Windows 11:

```powershell
# 1. Install WSL2 (run in an admin PowerShell, then reboot)
wsl --install
```

Then, inside your WSL2 / Linux / macOS shell, install the toolchain:

```bash
# Go 1.22+        https://go.dev/dl/
# Docker          https://docs.docker.com/get-docker/  (Docker Desktop on Win/Mac)
# kubectl         https://kubernetes.io/docs/tasks/tools/
# kind            go install sigs.k8s.io/kind@latest
# helm            https://helm.sh/docs/intro/install/

# Verify:
go version && docker --version && kubectl version --client && kind --version && helm version
```

> First build only: run `go mod tidy` to download dependencies and generate `go.sum`.

---

## Quickstart

```bash
# 0. Fetch deps (first time)
go mod tidy

# 1. Run the unit tests (envtest, no cluster needed)
make test

# 2. Spin up a local cluster, build + load the image, install the operator, apply a sample
make demo

# 3. Watch the operator reconcile desired → actual
kubectl get gpuworkloads -w
```

Expected:

```
NAME                 PHASE      DESIRED   AVAILABLE   GPUS   AGE
gpuworkload-sample   Pending    3         0           2      2s
gpuworkload-sample   Running    3         3           2      20s
```

Inspect the children the operator created (note the owner reference):

```bash
kubectl get deploy,svc -l gpu.kube-op-lite.io/workload=gpuworkload-sample
kubectl get deploy gpuworkload-sample -o jsonpath='{.metadata.ownerReferences[0].kind}'
```

Delete the workload — owner references cascade-delete the Deployment and Service:

```bash
kubectl delete gpuworkload gpuworkload-sample
```

---

## Common workflows

| Goal | Command |
|------|---------|
| Run controller locally (no webhooks) | `make run` |
| Run tests with coverage | `make test` |
| Build image | `make docker-build IMG=kube-op-lite:dev` |
| Load image into Kind | `make kind-load` |
| Install via Helm | `make helm-install` |
| Apply the sample | `make sample` |
| Regenerate CRD/RBAC after editing types | `make manifests` |
| Regenerate DeepCopy after editing types | `make generate` |
| Tear down cluster | `make kind-delete` |

---

## Lifecycle automation

- **Provision** — creating a `GpuWorkload` spins up a matching Deployment + Service.
- **Upgrade** — changing `spec.image` triggers a rolling update of the child Deployment.
- **Drain / Terminate** — on delete, a finalizer scales the Deployment to zero and holds the
  object in `Draining` until every pod has exited, reports `Terminated`, then releases it so
  owner-reference garbage collection removes the children.
- **Status reporting** — the controller writes back `phase`, `availableReplicas`, and
  conditions **only when they change**, and ignores its own status-only updates
  (`GenerationChangedPredicate`), so it never wakes itself up in a loop.

---

## Admission webhooks

- **Validating** — rejects `gpuCount > 8`, `replicas < 1`, and empty `image`.
- **Mutating** — defaults `priority` to `medium` and `replicas` to `1` when unset.

Webhooks need TLS certs, so they are **disabled by default** in the Helm chart. The
CRD's OpenAPI schema still enforces the same bounds. To enable the full webhooks:

```bash
# install cert-manager first
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
helm upgrade --install kube-op-lite charts/kube-op-lite \
  -n kube-op-lite-system --create-namespace \
  --set webhook.enabled=true
```

---

## How this maps to the skills it demonstrates

| Claim | Where |
|-------|-------|
| Custom Kubernetes controller in Go using Kubebuilder, defining CRDs | `api/v1alpha1`, `internal/controller` |
| Reconciliation loops and admission webhooks (GPU-operator patterns) | `gpuworkload_controller.go`, `gpuworkload_webhook.go` |
| Packaged via Helm charts for repeatable deployment | `charts/kube-op-lite/` |
| Cluster lifecycle automation (provision, drain, upgrade) | reconcile + finalizer |
| GPU-aware scheduling with priority preemption | `gpuResources`, `ensurePriorityClass`, `make preemption-demo` |
| Operator observability (Prometheus metrics) | `internal/controller/metrics.go` |
| Validated on a local Kind/Minikube cluster | `make demo` |

---

## How I built this (AI-assisted)

I used [Claude Code](https://claude.com/claude-code) as a pair programmer for this
project, the way I'd use any strong tool: I decide what gets built, it speeds up the typing.

**What I owned**
- The idea and the architecture: a Kubebuilder operator modeled on NVIDIA GPU Operator
  patterns (CRD, reconcile loop, webhooks, finalizer-based lifecycle, Helm, Kind).
- Scope and trade-off calls: real `nvidia.com/gpu` scheduling with fake node capacity
  instead of a label, PriorityClass-based preemption, and a Codespaces demo instead
  of a fake web mock, since an operator can't run on a static host.
- Reviewing every change, deciding what ships, and keeping the claims in this README
  honest (e.g. GPUs are advertised capacity, not real devices).

**What Claude Code helped with**
- Boilerplate, test scaffolding, and the first draft of new controller code.
- Code review that surfaced real bugs, which I then had fixed and covered by tests:
  a self-triggered reconcile loop from status writes, a drain that never actually
  scaled down, and envtest specs leaking finalized objects between runs.
- Running the build and test loop until `go vet` and all envtest specs passed.

How I worked with it: break the work into small, checkable steps, ask for options with
trade-offs before any code, demand evidence (passing tests, not "should work"), and
reject anything I couldn't explain myself.

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
