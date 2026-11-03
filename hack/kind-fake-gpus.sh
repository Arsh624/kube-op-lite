#!/usr/bin/env bash
# Advertise fake nvidia.com/gpu capacity on every node, the same extended
# resource the NVIDIA device plugin registers. The real kube-scheduler then
# counts GPUs and preempts by priority, with no GPU hardware needed.
#
#   usage: hack/kind-fake-gpus.sh [GPUS_PER_NODE]   (default 4)
set -euo pipefail

GPUS="${1:-4}"
for node in $(kubectl get nodes -o name); do
  kubectl patch "$node" --subresource=status --type=json \
    -p "[{\"op\":\"add\",\"path\":\"/status/capacity/nvidia.com~1gpu\",\"value\":\"${GPUS}\"}]" >/dev/null
  echo "${node#node/}: nvidia.com/gpu=${GPUS}"
done
