# CloudInfer

CloudInfer is an experimental control plane for LLM inference.

It provides a single OpenAI-compatible API endpoint that can sit in front of
multiple inference backends (cloud-managed, SaaS APIs, or self-hosted models),
with a focus on correctness, observability, and infrastructure-grade behavior.

CloudInfer is designed as a systems project — not a prompt framework,
agent toolkit, or model wrapper.

---

## What It Is

- An OpenAI-compatible gateway for chat-style LLM requests
- Streaming-first (SSE)
- Built in Go
- Designed to operate in hybrid cloud environments

---

## What It Is Not

- An agent framework
- A prompt management library
- A model hosting platform
- A UI or dashboard product
- A marketplace or API proxy

---

## Project Status

This project is in very early development.

APIs and internal behavior may change.
There are no stability guarantees at this time.

---

## Requirements

- Go 1.23+

CloudInfer currently requires Go 1.23 or newer due to
`google.golang.org/genai` and its transitive dependency stack.

---

## Quickstart

Run tests:

```bash
go test ./...
```

Start the server:

```bash
go run ./cmd/cloudinfer -config router.yaml
```

Send a chat-completions streaming request:

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"default","stream":true,"messages":[{"role":"user","content":"Say hello in 5 words."}]}'
```

Send a Responses API streaming request:

```bash
curl -N http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"default","stream":true,"input":[{"role":"user","content":"Say hello in 5 words."}]}'
```

---

## Responses API (supported subset)

CloudInfer v0.2.0-alpha supports a narrow, explicitly documented subset of
`POST /v1/responses`.

Supported request fields:

- `model`
- `input` as an array of message-like items with `role` plus either string
  `content` or text-part arrays (`type: "input_text"`)
- `stream`
- `max_output_tokens`
- `temperature`

Supported streaming semantic events:

- `response.created`
- `response.output_text.delta`
- `response.completed`
- `error`

Current scope limits:

- Text-only input and output
- No tools or function calling
- No multimodal inputs
- No reasoning traces or agent state
- No compatibility claims beyond the subset listed above

Migration note:

- Existing `POST /v1/chat/completions` support remains intact
- For new clients, prefer moving generation calls to `POST /v1/responses`
- The thin internal normalization layer keeps both endpoints routed through the
  same backend-selection path

Routing safety behavior:

- Before the first token is emitted, CloudInfer can abandon a slow backend and
  retry once against the next eligible backend when `routing.ttft_timeout_ms`
  is exceeded
- This fallback is strictly pre-token only; once any token has been streamed to
  the client, CloudInfer will not retry the request on another backend
- `rate_limited` failures can apply an explicit cooldown from upstream
  `Retry-After`
- Breaker cooldowns include bounded jitter so multi-instance failback is spread
  instead of synchronized

---

## Observability

CloudInfer exposes Prometheus metrics at `GET /metrics` with the exact
content type `text/plain; version=0.0.4`.

Current metrics:

- `cloudinfer_requests_total{endpoint,backend,status}`
- `cloudinfer_ttft_seconds_bucket{backend,model}`
- `cloudinfer_stream_duration_seconds_bucket{backend,model}`
- `cloudinfer_draining`

Label policy:

- `endpoint` is the API route template (`/v1/chat/completions` or `/v1/responses`)
- `backend` is the selected backend name, or `mock` when no backend is used
- `status` is the terminal request outcome (`ok`, `bad_request`, `rate_limited`, `timeout`, `auth_failed`, `upstream_error`, `draining`, and similar terminal states)
- `model` is the resolved response model
- No other application labels are allowed on these metrics

Useful PromQL examples:

```promql
sum by (backend, status) (rate(cloudinfer_requests_total[5m]))
```

```promql
histogram_quantile(0.95, sum by (le, backend, model) (rate(cloudinfer_ttft_seconds_bucket[5m])))
```

```promql
histogram_quantile(0.99, sum by (le, backend, model) (rate(cloudinfer_stream_duration_seconds_bucket[5m])))
```

```promql
max(cloudinfer_draining)
```

Safe debug endpoints:

- `GET /debug/config` returns sanitized config with masked secret values
- `GET /debug/routes` returns sanitized routing and backend state
- Debug endpoints are localhost-only by default
- Set `server_debug_expose: true` and `server_debug_auth_token_env: YOUR_ENV_VAR` to allow remote access when you explicitly need it
- Remote callers must send `Authorization: Bearer <token>` or `X-CloudInfer-Debug-Token: <token>`

---

## Kubernetes Deploy (v0.1.0-alpha)

Apply the dev overlay (router-only Deployment + ConfigMap + ServiceAccount):

```bash
kubectl apply -k deploy/overlays/dev/
```

Defaults:
- `livenessProbe` -> `GET /healthz`
- `readinessProbe` -> `GET /readyz`
- `terminationGracePeriodSeconds: 60` (overridable in overlays)

The long grace period plus readiness flip on SIGTERM give in-flight SSE streams time to drain instead of being cut mid-response.
Enable the optional ClusterIP Service by uncommenting `service.yaml` in `deploy/base/kustomization.yaml` or adding it as a resource in an overlay.

---

## Sidecar Mode

CloudInfer can run as a telemetry-driven inference control plane sidecar in Kubernetes.

See [docs/SIDECAR.md](docs/SIDECAR.md) for:

- sidecar deployment guidance
- reference Kubernetes manifests
- readiness and shutdown behavior
- metrics and debug endpoint usage

---

## Vertex (ADC) Setup

CloudInfer uses Application Default Credentials for Vertex AI. No API keys or
secrets are required in the application config.

Example backend config:

OpenAI-compatible upstream:

```yaml
routing:
  enabled: true
  policy: ewma_ttft
  cooldown_seconds: 15
  cooldown_jitter_fraction: 0.2
  ttft_timeout_ms: 1500

backends:
  - name: openai-primary
    type: openai
    openai:
      base_url: https://api.openai.com/v1
      api_key_env: OPENAI_API_KEY
      model: gpt-4o-mini
      timeout_seconds: 60
```

Vertex AI with ADC:

```yaml
routing:
  enabled: true
  policy: ewma_ttft
  cooldown_seconds: 15
  cooldown_jitter_fraction: 0.2
  ttft_timeout_ms: 1500

backends:
  - name: vertex-primary
    type: vertex
    vertex:
      project: your-gcp-project
      location: us-central1
      model: gemini-2.0-flash
```

Authenticate locally:

```bash
gcloud auth application-default login
```

Enable the Vertex AI API for your GCP project:

```bash
gcloud services enable aiplatform.googleapis.com
```

Routing config notes:

- `routing.ttft_timeout_ms` sets the pre-first-token watchdog; set `0` to
  disable the fallback
- `routing.cooldown_jitter_fraction` is a 0..1 multiplier applied around the
  base cooldown; `0.2` means the breaker cooldown is randomized within
  `80%` to `120%` of `routing.cooldown_seconds`

---

## Design Philosophy

CloudInfer prioritizes:

- Deterministic behavior
- Clear system boundaries
- Observable runtime characteristics
- Minimal abstraction

It is intentionally simple and infrastructure-oriented.

---

## License

Apache 2.0
