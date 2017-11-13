#!/bin/bash

set -e
if [ ! $1 ]; then
  IMAGE=gcr.io/unique-caldron-775/k8s-e2e:$(git rev-parse --short HEAD)
  echo "No image passed. Defaulting to $IMAGE"
else
  IMAGE=$1
  echo "Testing passed in image name: $IMAGE"
fi
set -x

docker run -e EXTRA_ARGS=-ginkgo.dryRun $IMAGE | grep -F "(Networking).*(\[Conformance\])|\[Feature:NetworkPolicy\]"
docker run -e EXTRA_ARGS=-ginkgo.dryRun -e FOCUS=customFocus $IMAGE | grep -F customFocus
docker run -e EXTRA_ARGS=-ginkgo.dryRun -e USE_EXT_CONFORMANCE_FOCUS=true $IMAGE | grep -F "(ConfigMap|Docker|Downward API|Events|DNS|Proxy|Scheduler|ReplicationController|ReplicaSet|CustomResourceDefinition).*(\[Conformance\])"
docker run -e EXTRA_ARGS=-ginkgo.dryRun -e USE_EXT_NETWORKING_FOCUS=true $IMAGE | grep -F "(Network|Pods|Services).*(\[Conformance\])|\[Feature:NetworkPolicy\]|\[Feature:Ingress\]"

