#!/usr/bin/env bash
# GPU preemption demo. Assumes `make demo` has run on a 1-node Kind cluster
# with 4 fake GPUs (make kind-gpus).
#
#   1. A low-priority job takes all 4 GPUs (2 pods x 2 GPUs) and goes Running.
#   2. A high-priority job asks for 2 GPUs. None are free, so the scheduler
#      evicts a low-priority pod to make room.
#   3. The low-priority job drops back to Pending with GpuUnschedulable=True
#      and the scheduler's reason ("Insufficient nvidia.com/gpu").
set -euo pipefail

apply() { # name replicas gpus priority
  kubectl apply -f - <<EOF
apiVersion: gpu.kube-op-lite.io/v1alpha1
kind: GpuWorkload
metadata:
  name: $1
spec:
  replicas: $2
  image: nginx:1.27
  gpuCount: $3
  priority: $4
EOF
}

wait_phase() { # name phase
  kubectl wait gpuworkload/"$1" --for=jsonpath='{.status.phase}'="$2" --timeout=120s
}

kubectl delete gpuworkloads --all --wait  # start with all GPUs free

echo ">> 1. low-priority training job takes all 4 GPUs"
apply training-low 2 2 low
wait_phase training-low Running
kubectl get gpuworkloads

echo ">> 2. high-priority inference job needs 2 GPUs"
apply inference-high 1 2 high
wait_phase inference-high Running

echo ">> 3. the scheduler preempted a low-priority pod"
wait_phase training-low Pending
kubectl get gpuworkloads
kubectl get gpuworkload training-low \
  -o jsonpath='{range .status.conditions[?(@.type=="GpuUnschedulable")]}GpuUnschedulable={.status}: {.message}{"\n"}{end}'
kubectl get pods -l app.kubernetes.io/managed-by=kube-op-lite \
  -o custom-columns=POD:.metadata.name,PRIORITY:.spec.priorityClassName,STATUS:.status.phase

echo ">> cleanup: kubectl delete gpuworkload training-low inference-high"
