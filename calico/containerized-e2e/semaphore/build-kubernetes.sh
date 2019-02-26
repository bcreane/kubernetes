#!/bin/bash
build/run.sh make WHAT=test/e2e/e2e.test
go build test/list/main.go
test/list/main test/e2e > calico/containerized-e2e/tests.txt
