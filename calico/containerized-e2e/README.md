# Containerized Kubernetes e2e's

### Usage


To connect to an unauthenticated k8s-apiserver running at `localhost:8080`:

```bash
docker run --net=host gcr.io/unique-caldron-775/k8s-e2e
```

To connect to another cluster, configure your local kubeconfig then volume mount it to `/root/kubeconfig`:

```bash
docker run --net=host -v ~/.kube/config:/root/kubeconfig gcr.io/unique-caldron-775/k8s-e2e
```

#### Selecting Tests

On startup, the container will read from a few environment variables to help facilitate test selection. By default,
it runs some common Network and Network Policy tests. 

To run a larger range of tests, set `USE_EXT_NETWORKING_FOCUS`

   ```
   docker run --net=host -e USE_EXT_NETWORKING_FOCUS=true gcr.io/unique-caldron-775/k8s-e2e
   ```

To run a large range of Conformance tests, set `USE_EXT_CONFORMANCE_FOCUS`

   ```
   docker run --net=host -e USE_EXT_CONFORMANCE_FOCUS=true gcr.io/unique-caldron-775/k8s-e2e
   ```

To use your own regex express, pass in a new `$FOCUS`:

  ```
  docker run --net=host -e FOCUS='Conformance' gcr.io/unique-caldron-775/k8s-e2e
  ```

### Building

1. Build e2e.test

   ```
   make WHAT=test/e2e/e2e.test
   ```

2. Build the docker image

   ```
   docker build -t gcr.io/unique-caldron-775/k8s-e2e $(GOPATH)/src/k8s.io/kubernetes
   ```

### Available Tags

- `:master` is built and pushed by semaphore automatically from the master branch.
- `:vX.Y-A` is pushed to match the github releases of this repo, where `vX.Y` represents
  the Kubernetes version this release was based off of, and `A` represents the release
  of the repo itself.
- `:vX.Y-latest` points to the most recent release for a particular kubernetes release.
- Each commit of master is tagged with the result of `git rev-parse --short HEAD`.

### Testing

Testing infrastructure is limited at the moment. See [test/test.sh](test/test.sh)
and don't hesitate to add more.
