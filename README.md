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

Send a streaming request:

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"default","stream":true,"messages":[{"role":"user","content":"Say hello in 5 words."}]}'
```

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

Authenticate locally:

```bash
gcloud auth application-default login
```

Enable the Vertex AI API for your GCP project:

```bash
gcloud services enable aiplatform.googleapis.com
```

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
