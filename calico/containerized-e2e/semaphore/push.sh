#!/bin/bash
GIT_SHA=$(git rev-parse --short HEAD)

if [ "${SEMAPHORE_THREAD_RESULT}" == "passed" ]; then
  docker push gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA}
  docker push gcr.io/unique-caldron-775/k8s-e2e:latest

  echo "[INFO] pushed gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA}"
  # TODO: change to :latest
  echo "[INFO] pushed gcr.io/unique-caldron-775/k8s-e2e:v2.0-alpha"
else
  echo "[ERROR] not pushing due to job result: ${SEMAPHORE_THREAD_RESULT}"
  exit 1
fi
