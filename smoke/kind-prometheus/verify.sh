#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
SMOKE_DIR="$ROOT_DIR/smoke/kind-prometheus"
TMP_DIR="$(mktemp -d)"
CLUSTER_NAME="${CLUSTER_NAME:-cloudinfer-smoke}"
NAMESPACE="${NAMESPACE:-cloudinfer-smoke}"
IMAGE_TAG="${IMAGE_TAG:-cloudinfer:smoke}"
PROMETHEUS_IMAGE="${PROMETHEUS_IMAGE:-prom/prometheus:v2.55.1}"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

require_bin() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required binary: $1" >&2
    exit 1
  fi
}

require_bin go
require_bin docker
require_bin kind
require_bin kubectl

if ! kind get clusters | grep -qx "$CLUSTER_NAME"; then
  kind create cluster --name "$CLUSTER_NAME"
fi

GOARCH_VALUE="${GOARCH:-$(go env GOARCH)}"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH_VALUE" go build -o "$TMP_DIR/cloudinfer" ./cmd/cloudinfer

cat >"$TMP_DIR/Dockerfile" <<'EOF'
FROM scratch
COPY cloudinfer /cloudinfer
ENTRYPOINT ["/cloudinfer"]
EOF

docker build -t "$IMAGE_TAG" "$TMP_DIR"
kind load docker-image --name "$CLUSTER_NAME" "$IMAGE_TAG"

kubectl get namespace "$NAMESPACE" >/dev/null 2>&1 || kubectl create namespace "$NAMESPACE"

sed "s|IMAGE_PLACEHOLDER|$IMAGE_TAG|g" "$SMOKE_DIR/cloudinfer.yaml" | kubectl apply -n "$NAMESPACE" -f -
kubectl apply -n "$NAMESPACE" -f "$SMOKE_DIR/prometheus-config.yaml"
sed "s|PROMETHEUS_IMAGE_PLACEHOLDER|$PROMETHEUS_IMAGE|g" "$SMOKE_DIR/prometheus.yaml" | kubectl apply -n "$NAMESPACE" -f -

kubectl wait -n "$NAMESPACE" --for=condition=available deployment/cloudinfer --timeout=180s
kubectl wait -n "$NAMESPACE" --for=condition=available deployment/prometheus --timeout=180s

prom_url_prefix="/api/v1/namespaces/$NAMESPACE/services/http:prometheus:9090/proxy"

wait_for_query() {
  local encoded_query="$1"
  local needle="$2"
  local attempts=0

  while [[ "$attempts" -lt 30 ]]; do
    local response
    response="$(kubectl get --raw "${prom_url_prefix}/api/v1/query?query=${encoded_query}")"
    if [[ "$response" == *"$needle"* ]]; then
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 2
  done

  echo "query did not converge: ${encoded_query}" >&2
  echo "last response: ${response}" >&2
  return 1
}

# Wait for the target to be discovered
echo "Waiting for Prometheus to discover the cloudinfer target..."
discovered=false
for i in {1..10}; do
  targets_json="$(kubectl get --raw "${prom_url_prefix}/api/v1/targets?state=active")"
  if [[ "$targets_json" == *'"job":"cloudinfer"'* ]]; then
    discovered=true
    break
  fi
  sleep 2
done

if [ "$discovered" = false ]; then
  echo "prometheus active targets missing cloudinfer job after retry" >&2
  echo "$targets_json" >&2
  exit 1
fi

wait_for_query 'up%7Bjob%3D%22cloudinfer%22%7D' '"result":[{'
wait_for_query 'up%7Bjob%3D%22cloudinfer%22%7D' ',"1"'
wait_for_query 'cloudinfer_draining' '"metric":{}'

echo "Prometheus scrape smoke test passed."
echo "Cluster: $CLUSTER_NAME"
echo "Namespace: $NAMESPACE"
echo "Image: $IMAGE_TAG"
