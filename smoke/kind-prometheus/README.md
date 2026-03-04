# kind Prometheus Smoke

This smoke check proves that a real Prometheus instance in a kind cluster can
scrape CloudInfer's `/metrics` endpoint successfully.

What it does:

- builds a local Linux container image from `./cmd/cloudinfer`
- creates a kind cluster if the named cluster does not exist
- loads the image into kind
- deploys CloudInfer in mock mode, with no external backend credentials
- deploys Prometheus configured to scrape `cloudinfer:8080/metrics`
- queries the Prometheus HTTP API through the Kubernetes service proxy
- verifies the `cloudinfer` target is up and `cloudinfer_draining` is present

Run it:

```bash
bash smoke/kind-prometheus/verify.sh
```

Useful overrides:

```bash
CLUSTER_NAME=my-cluster NAMESPACE=my-smoke IMAGE_TAG=cloudinfer:dev bash smoke/kind-prometheus/verify.sh
```

If your environment needs a different Prometheus image:

```bash
PROMETHEUS_IMAGE=prom/prometheus:v2.55.1 bash smoke/kind-prometheus/verify.sh
```

Cleanup after a run:

```bash
kubectl delete namespace cloudinfer-smoke
kind delete cluster --name cloudinfer-smoke
```
