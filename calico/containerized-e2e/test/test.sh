#!/bin/bash

set -e

function runner {
  local OPTS="$1"
  local VALIDATION="$2"
  echo "[INFO] docker run --rm $IMAGE $OPTS | $VALIDATION"
  out=$(docker run --rm $IMAGE $OPTS)
  status=$(echo $?)
  if [ "$status" -eq 0 ]; then
    echo "$out" | $VALIDATION
    status=$(echo $?)
    if [ "$status" -gt 0 ]; then
      exit "$status"
    fi
  fi
}

if [ ! "$1" ]; then
  IMAGE=gcr.io/unique-caldron-775/k8s-e2e:$(git rev-parse --short HEAD)
  echo "No image passed. Defaulting to $IMAGE"
else
  IMAGE="$1"
  echo "Testing passed in image name: $IMAGE"
fi

# runner <entrypoint cmds> <validation cmd>
runner '--extra-args -ginkgo.dryRun' 'grep -F "(Network|Pods|Services).*(\[Conformance\])|\[Feature:NetworkPolicy\]|\[Feature:Ingress\]"'
runner '--calico-version v2 --extra-args -ginkgo.dryRun' 'grep -F "\[Feature:CalicoPolicy-v2\]'
runner '--extended-networking true --extra-args -ginkgo.dryRun' 'grep -F "(Network|Pods|Services).*(\[Conformance\])|\[Feature:NetworkPolicy\]|\[Feature:Ingress\]"'
runner '--focus customFocus --extra-args -ginkgo.dryRun' 'grep -F customFocus'
