# CloudInfer User Education & Codelab Plan

## 1. Vision
The goal of CloudInfer education is to transition users from thinking about "API Proxying" to "Inference Control Planes." We aim to teach the **Sidecar-First** and **Telemetry-Driven** architectural patterns as the standard for production LLM infrastructure.

## 2. Target Personas
- **LLM Application Developers (The "Newbie"):** May have zero K8s knowledge. Focused on reliability, speed, and cost.
- **Backend Engineers:** Familiar with Docker/APIs, but not necessarily K8s experts.
- **Platform/SRE Engineers:** Focus on Kubernetes lifecycle, probes, metrics, and drain semantics.
- **Architects:** Focus on cost-vs-latency trade-offs and hybrid (local+cloud) strategies.

---

## 3. The "No-K8s Required" Primer
To support users with zero Kubernetes knowledge, we include a **"Concepts Bridge"** in the introductory materials:
- **"What is a Sidecar?":** An analogy-driven explanation (e.g., "The co-pilot in your application's car").
- **"Why Kubernetes?":** Explain that K8s is just a way to run many "cars" (Pods) and "co-pilots" (Sidecars) reliably at scale.
- **"The Pod":** A simple explanation that a Pod is just a group of containers that share the same network (localhost).

---

## 4. The Codelab Curriculum

### Module 1: "Hello Sidecar" (Introductory - No K8s Required)
**Status:** Planned
**Goal:** Run CloudInfer locally and stream a response in <2 minutes.
- **Concepts:** Binary execution, local config, mock mode, SSE (Server-Sent Events) format.
- **Hands-on:**
    - Start `cloudinfer` binary with a mock backend.
    - Use `curl` to observe token-by-token streaming.
    - Inspect `X-CloudInfer-Request-Id` headers.
- **Artifacts:** `codelab/01-hello-sidecar/router.yaml`.

### Module 2: "Sidecars with Docker Compose" (Bridge - Zero K8s Knowledge)
**Status:** Planned
**Goal:** Run an application and CloudInfer together using only Docker.
- **Concepts:** Multi-container orchestration, shared network namespaces, "localhost" as a bridge.
- **Hands-on:**
    - Use `docker-compose` to run a Python app + CloudInfer sidecar.
    - Point the app to `http://localhost:8080` and see routing in action.
    - This provides the "Sidecar" experience without the complexity of K8s.
- **Artifacts:** `codelab/02-docker-sidecar/docker-compose.yaml`.

### Module 3: "Kubernetes for the LLM Engineer" (Intermediate - K8s Intro)
**Status:** Planned
**Goal:** Deploy your first Pod with a CloudInfer sidecar.
- **Concepts:** Pods, Containers, Manifests (YAML), basic `kubectl`.
- **Hands-on:**
    - A "Slow-Walk" through a Kubernetes Pod manifest.
    - Use `kubectl apply -f` to deploy a sidecar into a local Kind cluster.
    - Verify `READY` logs and `/readyz` probe success.
- **Artifacts:** `codelab/03-k8s-basics/kind-config.yaml`, `deploy/base/pod-sidecar-example.yaml`.

### Module 4: "Observing the Mind of the Router" (Advanced)
**Status:** Planned
**Goal:** Visualize how CloudInfer makes real-time routing decisions.
- **Concepts:** EWMA TTFT, `/debug/routes`, Prometheus metrics labels.
- **Hands-on:**
    - Run CloudInfer with two backends (one fast, one artificially slow).
    - Watch the EWMA scores update in `/debug/routes`.
    - Trigger a "Provider Error" and observe the cooldown period.
- **Artifacts:** `codelab/04-telemetry-routing/docker-compose.yaml`, `chaos-test.sh`.

### Module 5: "The Invisible Hand: Safe Operations" (Expert)
**Status:** Planned
**Goal:** Perform a production rollout with zero dropped tokens.
- **Concepts:** SIGTERM drain semantics, `terminationGracePeriodSeconds`, rollout strategies.
- **Hands-on:**
    - Start a long-running 30s stream.
    - Trigger a deployment rollout (`kubectl rollout restart`).
    - Verify the sidecar enters `DRAINING` mode but finishes the active stream.
- **Artifacts:** `codelab/05-safe-ops/rollout-verification.sh`.

---

## 5. Delivery Format: The `/codelab` Directory
To ensure materials stay current, we will adopt a **"CI-Verified Docs"** approach once the codelabs are added:
- Each lab lives in `codelab/<name>/`.
- Each lab includes a `README.md` tutorial and a `verify.sh` script.
- **GitHub Actions** will run `verify.sh` for every lab on every PR to prevent documentation drift.

## 6. Success Metrics
- **Time-to-Ready:** A developer with zero K8s knowledge can run a sidecar pattern via Docker in <10 minutes.
- **Educational NPS:** Users report that the "K8s Primer" made the transition to the Kubernetes modules feel accessible.
- **Operational Clarity:** SREs can explain *why* a backend was bypassed using only `/debug/routes`.

## 7. Phase 1 Material Proposal (MVP)
1. **Tutorial:** "Your First Sidecar" (No K8s, No Docker, just the binary).
2. **Visual Guide:** "What is a Sidecar?" (Analogy-driven diagram).
3. **Reference:** "The Kubernetes Glossary for LLM Engineers."
