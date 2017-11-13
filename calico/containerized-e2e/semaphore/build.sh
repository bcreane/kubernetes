#!/bin/bash
GIT_SHA=$(git rev-parse --short HEAD)

docker build -t gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA} .
# TODO: change to latest
docker tag gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA} gcr.io/unique-caldron-775/k8s-e2e:v2.0-alpha

