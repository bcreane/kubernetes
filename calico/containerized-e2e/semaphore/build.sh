#!/bin/bash
GIT_SHA=$(git rev-parse --short HEAD)

docker build --quiet -t gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA} .
docker tag gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA} gcr.io/unique-caldron-775/k8s-e2e:master

