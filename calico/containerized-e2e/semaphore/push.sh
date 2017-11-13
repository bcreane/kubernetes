#!/bin/bash
GIT_SHA=$(git rev-parse --short HEAD)

if [ "${SEMAPHORE_THREAD_RESULT}" == "passed" ] && [ $BRANCH_NAME == "master" ]; then
  docker push gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA}
  docker push gcr.io/unique-caldron-775/k8s-e2e:master

  echo "[INFO] pushed gcr.io/unique-caldron-775/k8s-e2e:${GIT_SHA}"
  echo "[INFO] pushed gcr.io/unique-caldron-775/k8s-e2e:master"
else
  echo "[INFO] not pushing."
fi
