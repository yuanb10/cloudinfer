# CloudInfer Sidecar Mode

CloudInfer is designed to run well as a telemetry-driven inference control plane sidecar.

In sidecar mode, CloudInfer runs in the same Kubernetes pod as your application and exposes a local OpenAI-compatible endpoint. Your app sends requests to `http://127.0.0.1:8080/v1/chat/completions`, while CloudInfer handles backend selection, streaming normalization, telemetry, and metrics.

## Why Sidecar Mode

- Keeps inference traffic on the pod loopback interface
- Gives each workload its own routing state and TTFT history
- Simplifies app integration by presenting a single OpenAI-compatible endpoint
- Makes rollout and probe behavior match the application lifecycle

## Kubernetes Layout

Reference manifests live in `deploy/base`, with overlays in `deploy/overlays/{dev,prod}`.

Canonical install path:

```bash
kubectl apply -k deploy/overlays/dev/
```

Base manifests:

- [configmap.yaml](../deploy/base/configmap.yaml)
- [secret.yaml](../deploy/base/secret.yaml)
- [deployment.yaml](../deploy/base/deployment.yaml)
- [pod-sidecar-example.yaml](../deploy/base/pod-sidecar-example.yaml) (reference manifest only; not applied by the default kustomization)

The sidecar example mounts `router.yaml` from a ConfigMap, injects `OPENAI_API_KEY` from a Secret, and configures:

- `livenessProbe` on `GET /healthz`
- `readinessProbe` on `GET /readyz`
- small default resource requests and limits

## Backend Configuration

CloudInfer supports the same backend mix in sidecar mode as standalone mode:

- Vertex AI via Application Default Credentials
- OpenAI-compatible APIs via `base_url` and an API key environment variable

### Vertex AI

CloudInfer does not read static Vertex API keys from config. Use Kubernetes-native identity:

- GKE Workload Identity, or
- a mounted ADC credential file exposed through `GOOGLE_APPLICATION_CREDENTIALS`

Configure the backend in `router.yaml` with:

- `vertex.project`
- `vertex.location`
- `vertex.model`

### OpenAI-Compatible

Set:

- `openai.api_key_env` to the env var name, usually `OPENAI_API_KEY`
- `openai.base_url` to the provider endpoint
- `openai.model` to the provider model name

The example Secret is intentionally a placeholder. Replace `replace-me` with your actual key source at deploy time.

## Readiness and Shutdown

- `GET /healthz` means the process is alive.
- `GET /readyz` means CloudInfer can safely serve traffic without making external network probes.
- If no backends are configured, CloudInfer is ready in `mock` mode.
- If backends are configured, CloudInfer is ready when at least one backend initialized successfully.
- On `SIGTERM` or `SIGINT`, CloudInfer stops accepting new connections and allows in-flight streams to finish for the configured shutdown grace period.

## Verifying Routing

For streaming requests with initialized backends, inspect response headers:

- `X-CloudInfer-Backend`
- `X-CloudInfer-Route-Reason`
- `X-CloudInfer-Model`

Example:

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"default","stream":true,"messages":[{"role":"user","content":"Say hello in five words."}]}'
```

## Metrics and Debug Endpoints

CloudInfer exposes:

- `GET /metrics` for Prometheus
- `GET /debug/routes` for backend routing state, EWMA TTFT, cooldowns, and last observed status
- `GET /debug/config` for a sanitized runtime config view

`/debug/routes` is safe for operational introspection: it does not include prompt content or secrets.
