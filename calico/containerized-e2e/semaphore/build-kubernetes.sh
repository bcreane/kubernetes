#!/bin/bash
build/run.sh make WHAT=test/e2e/e2e.test
docker run -v $(pwd):/k8s $(ls _output/images) bash -c "go build /k8s/test/list/main.go ; ./main /k8s/test/e2e" > calico/containerized-e2e/tests.txt
