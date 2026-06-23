# Kube-Op-Lite

A lightweight Kubernetes **operator** written in Go that manages a custom resource
called `GpuWorkload`. Creating a `GpuWorkload` causes a controller to reconcile it
into real Kubernetes **Deployments** and **Services**, track its lifecycle through
defined phases, and enforce validation rules via **admission webhooks**.

It mirrors the patterns NVIDIA's GPU Operator uses — CRDs, controllers,
reconciliation loops, owner-reference lifecycle automation — in a self-contained,
locally-runnable form.

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
  gpuCount: 2          # simulated GPU request     (0–8, surfaced as pod label/annotation)
  priority: high       # low | medium | high       (drives a scheduling annotation)
```

| Field | Type | Rules |
|-------|------|-------|
| `spec.replicas`  | int  | required, ≥ 1 |
| `spec.image`     | string | required, non-empty |
| `spec.gpuCount`  | int  | 0–8 (rejected if > 8) |
| `spec.priority`  | enum | `low` / `medium` / `high`, defaults to `medium` |

**Status** reports `phase` (`Pending → Provisioning → Running → Draining → Terminated`),
`availableReplicas`, `conditions[]`, and `lastReconcileTime`.

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
- **Drain / Terminate** — a finalizer reports a `Draining` phase, then owner-reference
  garbage collection removes the children before the object is finalized.
- **Status reporting** — the controller writes back `phase`, `availableReplicas`, and an
  `Available` condition every reconcile, requeueing until actual state matches desired.

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
| Validated on a local Kind/Minikube cluster | `make demo` |

---

## License

Apache 2.0 — see [LICENSE](LICENSE).
