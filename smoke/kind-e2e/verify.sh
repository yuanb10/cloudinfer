#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
CLUSTER_NAME="${CLUSTER_NAME:-cloudinfer-e2e}"
NAMESPACE="${NAMESPACE:-cloudinfer-e2e}"
IMAGE_TAG="${IMAGE_TAG:-cloudinfer:e2e}"
PF_PID=""

cleanup() {
  if [[ -n "$PF_PID" ]]; then
    kill "$PF_PID" >/dev/null 2>&1 || true
    wait "$PF_PID" >/dev/null 2>&1 || true
  fi
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
require_bin curl

start_port_forward() {
  : >"$TMP_DIR/port-forward.log"
  kubectl port-forward -n "$NAMESPACE" service/cloudinfer 18080:http >"$TMP_DIR/port-forward.log" 2>&1 &
  PF_PID=$!
}

wait_for_http() {
  local url="$1"
  local attempts="${2:-20}"
  local delay="${3:-1}"

  for _ in $(seq 1 "$attempts"); do
    if curl -sf "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep "$delay"
  done

  return 1
}

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

kubectl apply -n "$NAMESPACE" -k deploy/overlays/dev
kubectl set image -n "$NAMESPACE" deployment/cloudinfer cloudinfer="$IMAGE_TAG"
kubectl rollout status -n "$NAMESPACE" deployment/cloudinfer --timeout=180s

start_port_forward

for _ in $(seq 1 60); do
  if grep -q "Forwarding from 127.0.0.1:18080" "$TMP_DIR/port-forward.log" 2>/dev/null && \
     curl -sf http://127.0.0.1:18080/readyz >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

stream_log="$TMP_DIR/stream.log"
(
  for _ in $(seq 1 10); do
    if curl -sfN \
      -H 'Content-Type: application/json' \
      -d '{"model":"default","stream":true,"messages":[{"role":"user","content":"hello"}]}' \
      http://127.0.0.1:18080/v1/chat/completions >>"$stream_log"; then
      printf '\n---\n' >>"$stream_log"
    else
      printf '\nstream_request_failed\n---\n' >>"$stream_log"
    fi
    sleep 1
  done
) &
load_pid=$!

sleep 2
kubectl rollout restart -n "$NAMESPACE" deployment/cloudinfer
kubectl rollout status -n "$NAMESPACE" deployment/cloudinfer --timeout=180s
wait "$load_pid" || true

kill "$PF_PID" >/dev/null 2>&1 || true
wait "$PF_PID" >/dev/null 2>&1 || true
PF_PID=""
start_port_forward

done_count="$(grep -c 'data: \[DONE\]' "$stream_log" || true)"
if [[ "$done_count" -lt 5 ]]; then
  echo "expected multiple completed streams during rollout, got $done_count" >&2
  cat "$stream_log" >&2
  exit 1
fi

metrics_headers="$TMP_DIR/metrics.headers"
if ! wait_for_http http://127.0.0.1:18080/metrics 20 1; then
  echo "metrics endpoint did not recover after rollout" >&2
  exit 1
fi
curl -sS -D "$metrics_headers" -o /dev/null http://127.0.0.1:18080/metrics
if ! grep -qi '^Content-Type: text/plain; version=0.0.4' "$metrics_headers"; then
  echo "metrics endpoint returned unexpected content-type" >&2
  cat "$metrics_headers" >&2
  exit 1
fi

if ! wait_for_http http://127.0.0.1:18080/readyz 20 1; then
  echo "readyz did not recover after rollout" >&2
  exit 1
fi

echo "kind e2e smoke passed"
echo "completed_streams_during_rollout=$done_count"
