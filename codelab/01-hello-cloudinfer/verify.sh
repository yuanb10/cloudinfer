#!/bin/bash
# verify.sh - Automated verification for Codelab 01

set -euo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$DIR/../.."

echo ">> Building cloudinfer binary..."
(cd "$PROJECT_ROOT" && go build -o "$DIR/cloudinfer" ./cmd/cloudinfer)

echo ">> Starting cloudinfer in the background..."
"$DIR/cloudinfer" -config "$DIR/router.yaml" > "$DIR/server.log" 2>&1 &
SERVER_PID=$!

# Ensure cleanup even if the test fails
trap 'if kill -0 "$SERVER_PID" 2>/dev/null; then kill "$SERVER_PID"; fi' EXIT

echo ">> Waiting for server to become ready..."
ATTEMPTS=0
while ! curl -s http://127.0.0.1:8080/healthz > /dev/null; do
  sleep 0.2
  ATTEMPTS=$((ATTEMPTS + 1))
  if [ $ATTEMPTS -gt 25 ]; then
    echo "FAILED: Server did not become ready within 5 seconds."
    cat "$DIR/server.log"
    exit 1
  fi
done

echo ">> Sending a test streaming request..."
RESPONSE=$(curl -N -s -D "$DIR/response.headers" http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "default", "stream": true, "messages": [{"role": "user", "content": "hi"}]}')

if [[ "$RESPONSE" == *"data: [DONE]"* ]]; then
  echo ">> SUCCESS: Streaming response received correctly."
else
  echo ">> FAILED: Streaming response did not complete."
  echo "Response: $RESPONSE"
  exit 1
fi

echo ">> Verifying request ID header..."
if grep -qi '^X-Request-Id:' "$DIR/response.headers"; then
  echo ">> SUCCESS: X-Request-Id header is present."
else
  echo ">> FAILED: X-Request-Id header is missing."
  echo "Headers:"
  cat "$DIR/response.headers"
  exit 1
fi

echo ">> Verifying readiness probe..."
READY_RESPONSE=$(curl -s http://127.0.0.1:8080/readyz)
if [[ "$READY_RESPONSE" == *'"ready":true'* ]]; then
  echo ">> SUCCESS: Readiness probe reports healthy."
else
  echo ">> FAILED: Readiness probe is unhealthy."
  echo "Response: $READY_RESPONSE"
  exit 1
fi

echo ">> ALL VERIFICATIONS PASSED."
