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

### Testing

gcr.io/unique-caldron-775/k8s-e2e 