How to run the e2e's on any cluster. What you'll need:

- A kubernetes cluster
- A host with:
    - A built `e2e.test` binary
    - Kubectl configured to talk to the kubernetes cluster

To get a Kubernetes cluster, there are a few popular options:

- Calico's vagrant cluster
- calico-ready-clusters (kubeadm or coreos)
- minikube

To get a built `e2e.test` binary:

0. checkout this repo
0. run `make WHAT=test/e2e/e2e.test`
0. Your e2e.test binary is now at: ./_output/local/go/bin/e2e.test

To get kubectl configured to talk to the kubernetes cluster, there are a few popular options:

- Point your kubectl at the cluster directly. See https://docs.projectcalico.org/v2.6/getting-started/kubernetes/installation/vagrant#2-configuring-the-cluster-and-kubectl. 
  
  >Note: This method may not always work with remote or authenticated clusters.

- Configure kubectl to connect to a cluster at localhost, then port-forward to your remote cluster. 
  Example: `gcloud compute --project=tigera-dev ssh ubuntu@turk-coreos-master -- -L localhost:8080:localhost:8080``

- Typically, the master node is configured out of the box. So copy `e2e.test` directly onto the host and run it there.

