# Use: docker run -v ~/.kube/config:/root/kubeconfig djosborne/k8s-e2e
# Override $FOCUS at runtime to select different tests
# Inspiration: https://github.com/kubernetes/kubernetes/blob/master/test/e2e_node/conformance/build/Dockerfile
FROM phusion/baseimage
LABEL maintainer "turk@tigera.io"
VOLUME /report
ADD calico/containerized-e2e/kubeconfig /root/kubeconfig
ADD ./_output/dockerized/bin/linux/amd64/e2e.test /usr/bin/
ADD calico/containerized-e2e/entrypoint.sh /entrypoint.sh
ADD calico/containerized-e2e/tests.txt /go/tests.txt
ADD calico/containerized-e2e/tests.txt /tests.txt

ENTRYPOINT ["/entrypoint.sh"]
CMD []
