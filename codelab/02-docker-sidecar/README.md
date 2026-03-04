# Codelab: Sidecars with Docker Compose

This lab is the bridge between "CloudInfer on my laptop" and "CloudInfer as a
sidecar." You will run a small app container and a CloudInfer container
together, then watch the app call `http://127.0.0.1:8080` exactly the way a
Pod-local sidecar pattern works in Kubernetes.

This codelab is split into two parts:

- Part 1: run the sidecar pattern in mock mode with no cloud credentials
- Part 2: keep the same setup, but switch CloudInfer to a real Vertex AI model

No Kubernetes knowledge is required. If you can run Docker, you can run this
lab.

---

## Who This Is For

- Developers who want the sidecar pattern without learning Kubernetes first
- Backend engineers validating localhost-based service-to-sidecar calls
- Platform engineers teaching the Pod-network mental model

---

## What You Will Learn

- How a sidecar pattern works outside Kubernetes
- Why "localhost" matters when two containers share one network namespace
- How CloudInfer behaves in mock mode when no real backends are configured
- How to check readiness and metrics from the host while an app talks to the
  sidecar

---

## Prerequisites

- Docker with `docker compose`
- A terminal with `curl`
- Run the commands below from the repository root

---

## Part 1: Sidecar Pattern in Mock Mode

Start here. This part is local, deterministic, and requires no API keys or
cloud setup.

### 1. Start the Lab

This lab uses:

- a `cloudinfer` service built from the repo's `Dockerfile`
- a tiny Python "app" container
- a shared network namespace so the app can call CloudInfer on `localhost`

Start everything:

```bash
docker compose -f ./codelab/02-docker-sidecar/docker-compose.yaml up --build
```

When the stack is ready, leave it running.

---

### 2. See the Sidecar Pattern

Open a second terminal and run the app client inside the `app` container:

```bash
docker compose -f ./codelab/02-docker-sidecar/docker-compose.yaml exec app \
  python /app/client.py
```

The client calls:

- `http://127.0.0.1:8080/v1/chat/completions`

That works because the `app` container shares the `cloudinfer` container's
network namespace. This mirrors the "same Pod, same localhost" property in
Kubernetes sidecar deployments.

You should see:

- `HTTP 200`
- an `X-Request-Id` header
- `data:` lines streamed back as SSE
- a final `data: [DONE]`

---

### 3. Inspect the Sidecar from the Host

Because the `cloudinfer` service publishes port `18080`, you can inspect it
from your host shell too:

Check health:

```bash
curl -s http://127.0.0.1:18080/healthz
```

Check readiness:

```bash
curl -s http://127.0.0.1:18080/readyz
```

Scrape metrics:

```bash
curl -i http://127.0.0.1:18080/metrics
```

The metrics response should include:

- `Content-Type: text/plain; version=0.0.4`
- `cloudinfer_draining`

---

### 4. What Just Happened?

This stack uses CloudInfer's built-in mock mode:

- no API keys
- no external providers
- deterministic local streaming output

The important architectural lesson is the container topology:

- The app and sidecar are separate containers
- The app still talks to `localhost`
- The host can still observe the sidecar over the published port

That is the same mental model you will use later in Kubernetes, where the Pod
network namespace replaces Docker Compose's shared service namespace.

---

## Part 2: Switch the Same Lab to a Real Vertex AI Model

Once Part 1 makes sense, use the same app-plus-sidecar layout with a live
Vertex AI backend.

### 5. Authenticate with ADC

If you want to replace mock mode with a real Vertex AI backend, you can layer in
the provided Compose override.

Authenticate locally with Application Default Credentials:

```bash
gcloud auth application-default login
```

### 6. Verify Local Credentials

Confirm the local ADC file exists, because the Compose override mounts this
exact file into the `cloudinfer` container:

```bash
ls ~/.config/gcloud/application_default_credentials.json
```

### 7. Export Vertex Settings

Export the required project ID plus any optional overrides:

```bash
export VERTEX_PROJECT="your-gcp-project"
```

Optional:

```bash
export VERTEX_LOCATION="us-central1"
export VERTEX_MODEL="gemini-2.5-flash-lite"
```

### 8. Start the Vertex-Backed Lab

Start the lab with the Vertex override:

```bash
docker compose \
  -f ./codelab/02-docker-sidecar/docker-compose.yaml \
  -f ./codelab/02-docker-sidecar/docker-compose.vertex.yaml \
  up --build
```

### 9. What Changes in Part 2?

This mounts your local ADC file into the `cloudinfer` container and configures
CloudInfer through environment variables:

- `BACKEND=vertex`
- `VERTEX_PROJECT` (required)
- `VERTEX_LOCATION` (optional, defaults to `us-central1`)
- `VERTEX_MODEL` (optional, defaults to `gemini-2.5-flash-lite`)

The recommended default is `gemini-2.5-flash-lite`, which Google currently
describes as its most cost-effective Gemini 2.5 model, and the current Vertex
AI pricing page lists it below Gemini 2.5 Flash and Gemini 2.5 Pro on text
token pricing.

If you want the older stable 2.0 line instead, use:

```bash
export VERTEX_MODEL="gemini-2.0-flash-lite-001"
```

Notes:

- The default `verify.sh` stays on mock mode because CI should not depend on
  live cloud credentials or billable APIs.
- With the Vertex override enabled, `/readyz` depends on successful backend
  initialization, so missing ADC or project permissions will leave the service
  unready until fixed.

---

## Verification

This lab includes an automated verifier:

```bash
bash ./codelab/02-docker-sidecar/verify.sh
```

It verifies Part 1 (mock mode). It does not use live Vertex credentials.

It will:

- build and start the Compose stack
- wait for CloudInfer to become healthy
- run the in-container app client
- confirm SSE completion and `X-Request-Id`
- confirm the metrics contract is exposed on the host port

---

## Cleanup

Stop and remove the stack:

```bash
docker compose -f ./codelab/02-docker-sidecar/docker-compose.yaml down -v
```

This prepares you for the next step: taking the same sidecar mental model into
Kubernetes.
