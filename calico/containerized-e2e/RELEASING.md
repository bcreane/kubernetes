# Release Process

Releases are cut using GCR web UI and Github UI. The most recent master image
built by semaphore is retagged to be the latest release.

### Resulting Artifacts

* `gcr.io/unique-caldron/k8s-e2e:$VERSION` container image.

### Requirements

* Semaphore should have built and pushed a master image.

### Creating the Release

0. [Draft a new release](https://github.com/tigera/kubernetes/releases/new). See
   previous releases for numbering scheme & recommended content.
0. Access the `k8s-e2e` directory of the
   [unique-caldron-775 container registry](https://console.cloud.google.com/gcr/images/unique-caldron-775?project=unique-caldron-775).
0. Edit the labels of the corresponding image, adding the release number (`vX.Y-A`) and `vX.Y-latest`.

