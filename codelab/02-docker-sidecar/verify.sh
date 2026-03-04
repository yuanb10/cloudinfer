#!/bin/bash
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$DIR/docker-compose.yaml"
OUTPUT_FILE="$DIR/client.out"

cleanup() {
  docker compose -f "$COMPOSE_FILE" down -v >/dev/null 2>&1 || true
}

trap cleanup EXIT

echo ">> Starting docker compose lab..."
docker compose -f "$COMPOSE_FILE" up -d --build

echo ">> Waiting for cloudinfer to become healthy..."
attempts=0
until curl -fsS http://127.0.0.1:18080/healthz >/dev/null; do
  sleep 1
  attempts=$((attempts + 1))
  if [[ "$attempts" -gt 30 ]]; then
    echo "FAILED: cloudinfer did not become healthy in time." >&2
    docker compose -f "$COMPOSE_FILE" logs
    exit 1
  fi
done

echo ">> Running in-container app request..."
docker compose -f "$COMPOSE_FILE" exec -T app python /app/client.py | tee "$OUTPUT_FILE"

if ! grep -q '^HTTP 200$' "$OUTPUT_FILE"; then
  echo "FAILED: app client did not receive HTTP 200." >&2
  exit 1
fi

if ! grep -q '^X-Request-Id:' "$OUTPUT_FILE"; then
  echo "FAILED: X-Request-Id header missing from app client output." >&2
  exit 1
fi

if ! grep -q 'data: \[DONE\]' "$OUTPUT_FILE"; then
  echo "FAILED: streamed response did not complete." >&2
  exit 1
fi

echo ">> Verifying readiness endpoint..."
if ! curl -fsS http://127.0.0.1:18080/readyz | grep -q '"ready":true'; then
  echo "FAILED: /readyz did not report ready=true." >&2
  exit 1
fi

echo ">> Verifying metrics contract..."
metrics_headers="$(curl -si http://127.0.0.1:18080/metrics)"
if [[ "$metrics_headers" != *"Content-Type: text/plain; version=0.0.4"* ]]; then
  echo "FAILED: /metrics did not return the Prometheus content type." >&2
  exit 1
fi

if [[ "$metrics_headers" != *"cloudinfer_draining"* ]]; then
  echo "FAILED: /metrics did not expose cloudinfer_draining." >&2
  exit 1
fi

echo ">> ALL VERIFICATIONS PASSED."
